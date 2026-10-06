// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/AChWorks/achrix"
	"github.com/jackc/pgx/v5"
)

var (
	ErrSiteTransfer        = errors.New("site transfer failed")
	ErrSiteTransferLimited = errors.New("site transfer limit exceeded")
	ErrSiteTransferUnknown = errors.New("site transfer activation outcome unknown")
)

const (
	siteTransferSchema         = "rixa.site-transfer.v1"
	siteTransferDBFile         = "database.dump"
	siteTransferMediaFile      = "media.tar"
	siteTransferPublicFile     = "public.tar"
	siteTransferManifestFile   = "manifest.json"
	siteTransferCompleteFile   = "complete.json"
	maxSiteTransferDumpBytes   = int64(2 << 30)
	maxSiteTransferArchiveBytes = int64(8 << 30)
	maxSiteTransferManifestBytes = int64(8 << 20)
	maxSiteTransferEntries     = 100000
	maxSiteTransferPathBytes   = 512
)

type SiteTransferTooling struct {
	PGDump    string
	PGRestore string
}

type SiteTransferArtifact struct {
	File   string `json:"file"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type SiteTransferArchiveEntry struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

type SiteTransferLedgerEntry struct {
	Version  int    `json:"version"`
	Identity string `json:"identity"`
}

type SiteTransferMediaAsset struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

type SiteTransferSummary struct {
	Accounts           int64  `json:"accounts"`
	AuditRecords       int64  `json:"audit_records"`
	ContentItems       int64  `json:"content_items"`
	ContentRevisions   int64  `json:"content_revisions"`
	ContentMediaRefs   int64  `json:"content_media_refs"`
	EditorialOperations int64 `json:"editorial_operations"`
	ReadyMedia         int64  `json:"ready_media"`
	SourceSessionRows  int64  `json:"source_session_rows"`
	AppearanceRevision int64  `json:"appearance_revision"`
	Theme              string `json:"theme"`
}

type SiteTransferSite struct {
	ID                   string             `json:"id"`
	Origin               string             `json:"origin"`
	AdminPrincipal       string             `json:"admin_principal"`
	PublicEnabled        bool               `json:"public_enabled"`
	PublicPolicy         PublicPolicyConfig `json:"public_policy,omitempty"`
	AuthorizationProfile string             `json:"authorization_profile"`
}

type SiteTransferManifest struct {
	Schema              string                               `json:"schema"`
	CapturedAt          time.Time                            `json:"captured_at"`
	RixaVersion         string                               `json:"rixa_version"`
	AChrixVersion       string                               `json:"achrix_version"`
	PostgreSQLVersionNum int                                  `json:"postgresql_server_version_num"`
	PGDumpVersion       string                               `json:"pg_dump_version"`
	Site                SiteTransferSite                     `json:"site"`
	SessionPolicy       string                               `json:"session_policy"`
	GlobalControlPolicy string                               `json:"global_control_policy"`
	Excluded            []string                             `json:"excluded"`
	Ledgers             map[string][]SiteTransferLedgerEntry `json:"ledgers"`
	Summary             SiteTransferSummary                  `json:"summary"`
	ReadyMedia          []SiteTransferMediaAsset             `json:"ready_media"`
	Database            SiteTransferArtifact                 `json:"database"`
	MediaArchive        SiteTransferArtifact                 `json:"media_archive"`
	MediaEntries        []SiteTransferArchiveEntry           `json:"media_entries"`
	PublicArchive       *SiteTransferArtifact                `json:"public_archive,omitempty"`
	PublicEntries       []SiteTransferArchiveEntry           `json:"public_entries,omitempty"`
}

type siteTransferComplete struct {
	ManifestSHA256 string `json:"manifest_sha256"`
}

type siteTransferSnapshot struct {
	PostgreSQLVersionNum int
	OtherSessions int64
	Unfinished    int64
	Ledgers       map[string][]SiteTransferLedgerEntry
	Summary       SiteTransferSummary
	ReadyMedia    []SiteTransferMediaAsset
}

func defaultSiteTransferTooling() SiteTransferTooling {
	dump := os.Getenv("RIXA_PG_DUMP")
	if dump == "" {
		dump = "pg_dump"
	}
	restore := os.Getenv("RIXA_PG_RESTORE")
	if restore == "" {
		restore = "pg_restore"
	}
	return SiteTransferTooling{PGDump: dump, PGRestore: restore}
}

func CaptureSite(parent context.Context, config Config, siteID, destination string, getenv func(string) (string, bool), tooling SiteTransferTooling) (SiteTransferManifest, error) {
	if err := config.validateStructure(); err != nil {
		return SiteTransferManifest{}, err
	}
	if err := validateSiteTransferCaptureDestination(config, destination); err != nil {
		return SiteTransferManifest{}, err
	}
	site, err := transferSiteConfig(config, siteID)
	if err != nil {
		return SiteTransferManifest{}, err
	}
	if site.AdminPrincipal == "" || !accountIDSyntax.MatchString(site.AdminPrincipal) {
		return SiteTransferManifest{}, fmt.Errorf("%w: source site administrator", ErrSiteTransfer)
	}
	if err = createPrivateTransferDirectory(destination); err != nil {
		return SiteTransferManifest{}, err
	}
	if getenv == nil {
		getenv = os.LookupEnv
	}
	target, err := config.BootstrapDatabase("site:"+siteID, getenv)
	if err != nil {
		return SiteTransferManifest{}, err
	}
	pgEnv, _, err := siteTransferPostgresEnvironment(target.DSN)
	if err != nil {
		return SiteTransferManifest{}, err
	}
	if tooling.PGDump == "" {
		tooling = defaultSiteTransferTooling()
	}
	if tooling.PGDump == "" {
		return SiteTransferManifest{}, fmt.Errorf("%w: pg_dump unavailable", ErrSiteTransfer)
	}

	mediaUnlock, err := lockTransferRoot(site.MediaRoot)
	if err != nil {
		return SiteTransferManifest{}, fmt.Errorf("%w: media root is not quiescent", ErrSiteTransfer)
	}
	defer mediaUnlock()

	var publicUnlock func()
	if site.PublicRoot != "" {
		publicUnlock, err = lockTransferRoot(site.PublicRoot)
		if err != nil {
			return SiteTransferManifest{}, fmt.Errorf("%w: public root is not quiescent", ErrSiteTransfer)
		}
		defer publicUnlock()
	}

	fence, before, err := beginSiteTransferDatabaseFence(parent, target.DSN, site.AdminPrincipal)
	if err != nil {
		return SiteTransferManifest{}, err
	}
	defer func() { _ = fence.Close(context.Background()) }()
	if before.OtherSessions != 0 {
		return SiteTransferManifest{}, fmt.Errorf("%w: source database has %d other sessions; stop ingress/runtime first", ErrSiteTransfer, before.OtherSessions)
	}
	if before.Unfinished != 0 {
		return SiteTransferManifest{}, fmt.Errorf("%w: source Media has unfinished state; reconcile before capture", ErrSiteTransfer)
	}

	pgDumpVersion, err := siteTransferToolVersion(parent, tooling.PGDump, pgEnv)
	if err != nil || !siteTransferPostgres18Tool(pgDumpVersion) {
		return SiteTransferManifest{}, fmt.Errorf("%w: pg_dump must be compatible PostgreSQL 18 tooling", ErrSiteTransfer)
	}
	dbArtifact, err := runSiteTransferDump(parent, tooling.PGDump, pgEnv, filepath.Join(destination, siteTransferDBFile))
	if err != nil {
		return SiteTransferManifest{}, err
	}
	mediaArtifact, mediaEntries, err := archiveSiteTransferRoot(site.MediaRoot, filepath.Join(destination, siteTransferMediaFile))
	if err != nil {
		return SiteTransferManifest{}, err
	}
	if err = verifyMediaTransferEntries(before.ReadyMedia, mediaEntries); err != nil {
		return SiteTransferManifest{}, err
	}

	var publicArtifact *SiteTransferArtifact
	var publicEntries []SiteTransferArchiveEntry
	if site.PublicRoot != "" {
		artifact, entries, archiveErr := archiveSiteTransferRoot(site.PublicRoot, filepath.Join(destination, siteTransferPublicFile))
		if archiveErr != nil {
			return SiteTransferManifest{}, archiveErr
		}
		publicArtifact = &artifact
		publicEntries = entries
	}

	after, err := inspectSiteTransferDatabaseQuery(parent, fence, site.AdminPrincipal)
	if err != nil {
		return SiteTransferManifest{}, err
	}
	if after.OtherSessions != 0 {
		return SiteTransferManifest{}, fmt.Errorf("%w: source database changed session state during capture", ErrSiteTransfer)
	}
	if !equalSourceTransferSnapshots(before, after) {
		return SiteTransferManifest{}, fmt.Errorf("%w: source state changed during capture", ErrSiteTransfer)
	}
	finalMediaEntries, err := inspectSiteTransferRoot(site.MediaRoot)
	if err != nil || !reflect.DeepEqual(finalMediaEntries, mediaEntries) {
		return SiteTransferManifest{}, fmt.Errorf("%w: Media root changed after archive capture", ErrSiteTransfer)
	}
	if site.PublicRoot != "" {
		finalPublicEntries, publicErr := inspectSiteTransferRoot(site.PublicRoot)
		if publicErr != nil || !reflect.DeepEqual(finalPublicEntries, publicEntries) {
			return SiteTransferManifest{}, fmt.Errorf("%w: public root changed after archive capture", ErrSiteTransfer)
		}
	}

	manifest := SiteTransferManifest{
		Schema:        siteTransferSchema,
		CapturedAt:    time.Now().UTC().Truncate(time.Microsecond),
		RixaVersion:          Version,
		AChrixVersion:        achrix.Version(),
		PostgreSQLVersionNum: before.PostgreSQLVersionNum,
		PGDumpVersion:        pgDumpVersion,
		Site: SiteTransferSite{
			ID: site.ID, Origin: site.Origin, AdminPrincipal: site.AdminPrincipal,
			PublicEnabled: site.PublicRoot != "", PublicPolicy: site.PublicPolicy,
			AuthorizationProfile: "rixa.fixed-site-admin-v1",
		},
		SessionPolicy:       "excluded-at-capture",
		GlobalControlPolicy: "not-captured-target-must-recreate-and-remap",
		Excluded: []string{
			"database DSN and credentials",
			"TLS private keys and certificates",
			"control database and control administrator",
			"identity session rows",
			"deployment-specific private-root paths",
		},
		Ledgers:       before.Ledgers,
		Summary:       before.Summary,
		ReadyMedia:    before.ReadyMedia,
		Database:      dbArtifact,
		MediaArchive:  mediaArtifact,
		MediaEntries:  mediaEntries,
		PublicArchive: publicArtifact,
		PublicEntries: publicEntries,
	}
	if err = validateSiteTransferManifest(manifest); err != nil {
		return SiteTransferManifest{}, err
	}
	if err = writeSiteTransferMetadata(destination, manifest); err != nil {
		return SiteTransferManifest{}, err
	}
	return manifest, nil
}

func RestoreSite(parent context.Context, config Config, siteID, source string, getenv func(string) (string, bool), tooling SiteTransferTooling) (SiteTransferManifest, error) {
	if err := config.validateStructure(); err != nil {
		return SiteTransferManifest{}, err
	}
	site, err := transferSiteConfig(config, siteID)
	if err != nil {
		return SiteTransferManifest{}, err
	}
	if err = validateSiteTransferRestoreSource(source, site); err != nil {
		return SiteTransferManifest{}, err
	}
	manifest, err := readSiteTransferMetadata(source)
	if err != nil {
		return SiteTransferManifest{}, err
	}
	if err = validateSiteTransferCompatibility(site, manifest); err != nil {
		return SiteTransferManifest{}, err
	}
	if getenv == nil {
		getenv = os.LookupEnv
	}
	target, err := config.BootstrapDatabase("site:"+siteID, getenv)
	if err != nil {
		return SiteTransferManifest{}, err
	}
	pgEnv, databaseName, err := siteTransferPostgresEnvironment(target.DSN)
	if err != nil {
		return SiteTransferManifest{}, err
	}
	if tooling.PGRestore == "" {
		tooling = defaultSiteTransferTooling()
	}
	if tooling.PGRestore == "" {
		return SiteTransferManifest{}, fmt.Errorf("%w: pg_restore unavailable", ErrSiteTransfer)
	}
	pgRestoreVersion, versionErr := siteTransferToolVersion(parent, tooling.PGRestore, pgEnv)
	if versionErr != nil || !siteTransferPostgres18Tool(pgRestoreVersion) {
		return SiteTransferManifest{}, fmt.Errorf("%w: pg_restore must be compatible PostgreSQL 18 tooling", ErrSiteTransfer)
	}

	if err = ensureEmptySiteTransferDatabase(parent, target.DSN, manifest.PostgreSQLVersionNum); err != nil {
		return SiteTransferManifest{}, err
	}
	if err = ensureNewTransferRootTarget(site.MediaRoot); err != nil {
		return SiteTransferManifest{}, err
	}
	if site.PublicRoot != "" {
		if err = ensureNewTransferRootTarget(site.PublicRoot); err != nil {
			return SiteTransferManifest{}, err
		}
	}

	if err = verifySiteTransferArtifact(source, manifest.Database, maxSiteTransferDumpBytes); err != nil {
		return SiteTransferManifest{}, err
	}
	if err = verifySiteTransferArtifact(source, manifest.MediaArchive, maxSiteTransferArchiveBytes); err != nil {
		return SiteTransferManifest{}, err
	}
	if manifest.PublicArchive != nil {
		if err = verifySiteTransferArtifact(source, *manifest.PublicArchive, maxSiteTransferArchiveBytes); err != nil {
			return SiteTransferManifest{}, err
		}
	}

	mediaStage, err := newTransferStage(site.MediaRoot)
	if err != nil {
		return SiteTransferManifest{}, err
	}
	defer func() { _ = os.RemoveAll(mediaStage) }()
	if err = extractSiteTransferArchive(filepath.Join(source, manifest.MediaArchive.File), mediaStage, manifest.MediaEntries); err != nil {
		return SiteTransferManifest{}, err
	}
	if err = verifyMediaTransferEntries(manifest.ReadyMedia, manifest.MediaEntries); err != nil {
		return SiteTransferManifest{}, err
	}

	publicStage := ""
	if manifest.PublicArchive != nil {
		publicStage, err = newTransferStage(site.PublicRoot)
		if err != nil {
			return SiteTransferManifest{}, err
		}
		defer func() { _ = os.RemoveAll(publicStage) }()
		if err = extractSiteTransferArchive(filepath.Join(source, manifest.PublicArchive.File), publicStage, manifest.PublicEntries); err != nil {
			return SiteTransferManifest{}, err
		}
	}

	if err = runSiteTransferRestore(parent, tooling.PGRestore, pgEnv, databaseName, filepath.Join(source, manifest.Database.File)); err != nil {
		return SiteTransferManifest{}, err
	}

	restored, err := inspectSiteTransferDatabase(parent, target.DSN, site.AdminPrincipal)
	if err != nil {
		return SiteTransferManifest{}, err
	}
	if restored.OtherSessions != 0 || restored.Unfinished != 0 {
		return SiteTransferManifest{}, fmt.Errorf("%w: restored database is not quiescent", ErrSiteTransfer)
	}
	if restored.Summary.SourceSessionRows != 0 {
		return SiteTransferManifest{}, fmt.Errorf("%w: restored identity sessions were not cleared", ErrSiteTransfer)
	}
	if !equalRestoredSiteTransferSnapshot(manifest, restored) {
		return SiteTransferManifest{}, fmt.Errorf("%w: restored database does not match capture manifest", ErrSiteTransfer)
	}
	stageMediaEntries, err := inspectSiteTransferRoot(mediaStage)
	if err != nil || !reflect.DeepEqual(stageMediaEntries, manifest.MediaEntries) {
		return SiteTransferManifest{}, fmt.Errorf("%w: staged Media tree verification", ErrSiteTransfer)
	}
	if err = verifyMediaTransferEntries(restored.ReadyMedia, stageMediaEntries); err != nil {
		return SiteTransferManifest{}, err
	}
	if publicStage != "" {
		stagePublicEntries, inspectErr := inspectSiteTransferRoot(publicStage)
		if inspectErr != nil || !reflect.DeepEqual(stagePublicEntries, manifest.PublicEntries) {
			return SiteTransferManifest{}, fmt.Errorf("%w: staged public tree verification", ErrSiteTransfer)
		}
	}
	if err = verifyRestoredSiteTransferContracts(parent, config, site, target.DSN, mediaStage, publicStage); err != nil {
		return SiteTransferManifest{}, err
	}

	if err = activateSiteTransferRoots(mediaStage, site.MediaRoot, publicStage, site.PublicRoot); err != nil {
		return SiteTransferManifest{}, err
	}
	mediaStage = ""
	publicStage = ""

	finalMedia, err := inspectSiteTransferRoot(site.MediaRoot)
	if err != nil || !reflect.DeepEqual(finalMedia, manifest.MediaEntries) {
		return SiteTransferManifest{}, fmt.Errorf("%w: activated Media tree verification", ErrSiteTransfer)
	}
	if site.PublicRoot != "" {
		finalPublic, publicErr := inspectSiteTransferRoot(site.PublicRoot)
		if publicErr != nil || !reflect.DeepEqual(finalPublic, manifest.PublicEntries) {
			return SiteTransferManifest{}, fmt.Errorf("%w: activated public tree verification", ErrSiteTransfer)
		}
	}
	return manifest, nil
}

func verifyRestoredSiteTransferContracts(parent context.Context, config Config, site SiteConfig, dsn, mediaRoot, publicRoot string) (result error) {
	admin, err := Principal(site.AdminPrincipal)
	if err != nil {
		return fmt.Errorf("%w: restored site administrator identity", ErrSiteTransfer)
	}
	verifySite := site
	verifySite.MediaRoot = mediaRoot
	if verifySite.PublicRoot != "" {
		if publicRoot == "" {
			return fmt.Errorf("%w: restored public staging root", ErrSiteTransfer)
		}
		verifySite.PublicRoot = publicRoot
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runtime, err := buildSite(config, ResolvedSite{SiteConfig: verifySite, DSN: dsn}, admin, admin, logger)
	if err != nil {
		return fmt.Errorf("%w: restored site composition", ErrSiteTransfer)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		defer stopCancel()
		if runtime.Publication != nil {
			result = errors.Join(result, runtime.Publication.Stop(stopCtx))
		}
		result = errors.Join(result, runtime.App.Shutdown(stopCtx))
	}()

	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	if err = runtime.App.Start(ctx); err != nil {
		return fmt.Errorf("%w: restored site application start", ErrSiteTransfer)
	}
	if err = runtime.App.Ready(ctx); err != nil {
		return fmt.Errorf("%w: restored site application readiness", ErrSiteTransfer)
	}
	if err = runtime.Editorial.Ready(ctx); err != nil {
		return fmt.Errorf("%w: restored Rixa editorial readiness", ErrSiteTransfer)
	}
	if runtime.Publication != nil {
		if err = runtime.Publication.Ready(); err != nil {
			return fmt.Errorf("%w: restored public publication readiness", ErrSiteTransfer)
		}
	}
	return nil
}

func transferSiteConfig(config Config, siteID string) (SiteConfig, error) {
	if !siteIDSyntax.MatchString(siteID) {
		return SiteConfig{}, fmt.Errorf("%w: site identity", ErrSiteTransfer)
	}
	for _, site := range config.Sites {
		if site.ID == siteID && !site.Disabled {
			return site, nil
		}
	}
	return SiteConfig{}, fmt.Errorf("%w: active site not found", ErrSiteTransfer)
}

func validateSiteTransferCompatibility(site SiteConfig, manifest SiteTransferManifest) error {
	if err := validateSiteTransferManifest(manifest); err != nil {
		return err
	}
	if manifest.RixaVersion != Version || manifest.AChrixVersion != achrix.Version() {
		return fmt.Errorf("%w: incompatible source dependency identity", ErrSiteTransfer)
	}
	if manifest.Site.ID != site.ID || manifest.Site.Origin != site.Origin ||
		manifest.Site.AdminPrincipal != site.AdminPrincipal ||
		manifest.Site.PublicEnabled != (site.PublicRoot != "") ||
		!reflect.DeepEqual(manifest.Site.PublicPolicy, site.PublicPolicy) {
		return fmt.Errorf("%w: target site identity/configuration differs from capture", ErrSiteTransfer)
	}
	return nil
}

func validateSiteTransferManifest(manifest SiteTransferManifest) error {
	if manifest.Schema != siteTransferSchema || manifest.CapturedAt.IsZero() ||
		manifest.RixaVersion == "" || manifest.AChrixVersion == "" ||
		manifest.PostgreSQLVersionNum/10000 != 18 || !siteTransferPostgres18Tool(manifest.PGDumpVersion) ||
		!siteIDSyntax.MatchString(manifest.Site.ID) ||
		!accountIDSyntax.MatchString(manifest.Site.AdminPrincipal) ||
		manifest.Site.AuthorizationProfile != "rixa.fixed-site-admin-v1" ||
		manifest.SessionPolicy != "excluded-at-capture" ||
		manifest.GlobalControlPolicy != "not-captured-target-must-recreate-and-remap" ||
		len(manifest.Excluded) == 0 {
		return fmt.Errorf("%w: invalid transfer manifest", ErrSiteTransfer)
	}
	if manifest.Site.PublicEnabled {
		if manifest.PublicArchive == nil || manifest.Site.PublicPolicy.validate() != nil {
			return fmt.Errorf("%w: public transfer manifest", ErrSiteTransfer)
		}
	} else if manifest.PublicArchive != nil || len(manifest.PublicEntries) != 0 || !manifest.Site.PublicPolicy.empty() {
		return fmt.Errorf("%w: unexpected public transfer state", ErrSiteTransfer)
	}
	for _, artifact := range append([]SiteTransferArtifact{manifest.Database, manifest.MediaArchive}, optionalTransferArtifact(manifest.PublicArchive)...) {
		if !validSiteTransferArtifact(artifact) {
			return fmt.Errorf("%w: invalid transfer artifact", ErrSiteTransfer)
		}
	}
	if len(manifest.MediaEntries) > maxSiteTransferEntries || len(manifest.PublicEntries) > maxSiteTransferEntries {
		return ErrSiteTransferLimited
	}
	if int64(len(manifest.ReadyMedia)) != manifest.Summary.ReadyMedia {
		return fmt.Errorf("%w: Media manifest count mismatch", ErrSiteTransfer)
	}
	for _, ledger := range []string{"identity", "audit", "media", "rixa"} {
		if len(manifest.Ledgers[ledger]) == 0 {
			return fmt.Errorf("%w: missing %s migration ledger", ErrSiteTransfer, ledger)
		}
	}
	return nil
}

func optionalTransferArtifact(value *SiteTransferArtifact) []SiteTransferArtifact {
	if value == nil {
		return nil
	}
	return []SiteTransferArtifact{*value}
}

func validSiteTransferArtifact(artifact SiteTransferArtifact) bool {
	if artifact.File == "" || filepath.Base(artifact.File) != artifact.File ||
		artifact.Size < 1 || len(artifact.SHA256) != 64 {
		return false
	}
	_, err := hex.DecodeString(artifact.SHA256)
	return err == nil
}

func validateSiteTransferCaptureDestination(config Config, destination string) error {
	if err := validateDeclaredMediaRoots(config.Sites); err != nil {
		return err
	}
	if err := validateNewSiteTransferDirectoryPath(destination); err != nil {
		return err
	}
	for _, site := range config.Sites {
		if site.Disabled {
			continue
		}
		for _, root := range []string{site.MediaRoot, site.PublicRoot} {
			if root != "" && pathsOverlap(destination, root) {
				return fmt.Errorf("%w: transfer destination overlaps private site root", ErrSiteTransfer)
			}
		}
	}
	return nil
}

func validateSiteTransferRestoreSource(source string, site SiteConfig) error {
	if source == "" || !filepath.IsAbs(source) || filepath.Clean(source) != source || source == string(filepath.Separator) {
		return fmt.Errorf("%w: transfer source path", ErrSiteTransfer)
	}
	canonical, err := filepath.EvalSymlinks(source)
	if err != nil || canonical != source {
		return fmt.Errorf("%w: transfer source path", ErrSiteTransfer)
	}
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: transfer source directory", ErrSiteTransfer)
	}
	for _, target := range []string{site.MediaRoot, site.PublicRoot} {
		if target != "" && pathsOverlap(source, target) {
			return fmt.Errorf("%w: transfer source overlaps restore root", ErrSiteTransfer)
		}
	}
	return nil
}

func validateNewSiteTransferDirectoryPath(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return fmt.Errorf("%w: transfer destination path", ErrSiteTransfer)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: transfer destination already exists", ErrSiteTransfer)
	}
	parent := filepath.Dir(path)
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil || canonical != parent {
		return fmt.Errorf("%w: transfer destination parent", ErrSiteTransfer)
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: transfer destination parent", ErrSiteTransfer)
	}
	return nil
}

func createPrivateTransferDirectory(path string) error {
	if err := validateNewSiteTransferDirectoryPath(path); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return fmt.Errorf("%w: create new transfer destination", ErrSiteTransfer)
	}
	return nil
}

func lockTransferRoot(path string) (func(), error) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSiteTransfer
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrSiteTransfer
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, ErrSiteTransfer
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

func siteTransferPostgresEnvironment(dsn string) ([]string, string, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg == nil || cfg.Host == "" || cfg.User == "" || cfg.Database == "" ||
		len(cfg.Fallbacks) != 0 || cfg.TLSConfig != nil || !siteTransferLocalHost(cfg.Host) {
		return nil, "", fmt.Errorf("%w: transfer supports only one explicit local PostgreSQL endpoint with sslmode=disable", ErrSiteTransfer)
	}
	env := []string{
		"PGHOST=" + cfg.Host,
		"PGPORT=" + strconv.Itoa(int(cfg.Port)),
		"PGUSER=" + cfg.User,
		"PGPASSWORD=" + cfg.Password,
		"PGDATABASE=" + cfg.Database,
		"PGSSLMODE=disable",
		"PGCONNECT_TIMEOUT=3",
		"LANG=C",
		"LC_ALL=C",
	}
	return env, cfg.Database, nil
}

func siteTransferLocalHost(host string) bool {
	if strings.HasPrefix(host, "/") || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type siteTransferDatabaseQuery interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type siteTransferDatabaseFence struct {
	conn *pgx.Conn
	tx   pgx.Tx
}

func (f *siteTransferDatabaseFence) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return f.tx.Query(ctx, sql, args...)
}

func (f *siteTransferDatabaseFence) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return f.tx.QueryRow(ctx, sql, args...)
}

func (f *siteTransferDatabaseFence) Close(ctx context.Context) error {
	if f == nil {
		return nil
	}
	var err error
	if f.tx != nil {
		err = errors.Join(err, f.tx.Rollback(ctx))
		f.tx = nil
	}
	if f.conn != nil {
		err = errors.Join(err, f.conn.Close(ctx))
		f.conn = nil
	}
	return err
}

func beginSiteTransferDatabaseFence(parent context.Context, dsn, adminPrincipal string) (*siteTransferDatabaseFence, siteTransferSnapshot, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg == nil || !siteTransferLocalHost(cfg.Host) || len(cfg.Fallbacks) != 0 || cfg.TLSConfig != nil {
		return nil, siteTransferSnapshot{}, fmt.Errorf("%w: source database profile", ErrSiteTransfer)
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, siteTransferSnapshot{}, fmt.Errorf("%w: inspect database", ErrSiteTransfer)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		_ = conn.Close(context.Background())
		return nil, siteTransferSnapshot{}, fmt.Errorf("%w: begin database capture fence", ErrSiteTransfer)
	}
	fence := &siteTransferDatabaseFence{conn: conn, tx: tx}
	fail := func(e error) (*siteTransferDatabaseFence, siteTransferSnapshot, error) {
		_ = fence.Close(context.Background())
		return nil, siteTransferSnapshot{}, e
	}
	if _, err = tx.Exec(ctx, "SET LOCAL lock_timeout='2s'"); err != nil {
		return fail(fmt.Errorf("%w: configure database capture fence", ErrSiteTransfer))
	}
	const lockSQL = `
		LOCK TABLE
			identity.schema_migrations, identity.accounts, identity.credentials, identity.sessions,
			audit.schema_migrations, audit.records,
			media.schema_migrations, media.assets,
			rixa.schema_migrations, rixa.content_items, rixa.content_revisions, rixa.content_media_refs,
			rixa.appearance_revisions, rixa.appearance_head, rixa.operations
		IN SHARE MODE
	`
	if _, err = tx.Exec(ctx, lockSQL); err != nil {
		return fail(fmt.Errorf("%w: source database is not quiescent", ErrSiteTransfer))
	}
	snapshot, err := inspectSiteTransferDatabaseQuery(parent, fence, adminPrincipal)
	if err != nil {
		return fail(err)
	}
	return fence, snapshot, nil
}

func inspectSiteTransferDatabase(parent context.Context, dsn, adminPrincipal string) (siteTransferSnapshot, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg == nil || !siteTransferLocalHost(cfg.Host) || len(cfg.Fallbacks) != 0 || cfg.TLSConfig != nil {
		return siteTransferSnapshot{}, fmt.Errorf("%w: source database profile", ErrSiteTransfer)
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return siteTransferSnapshot{}, fmt.Errorf("%w: inspect database", ErrSiteTransfer)
	}
	defer conn.Close(context.Background())
	return inspectSiteTransferDatabaseQuery(ctx, conn, adminPrincipal)
}

func inspectSiteTransferDatabaseQuery(parent context.Context, query siteTransferDatabaseQuery, adminPrincipal string) (siteTransferSnapshot, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	var err error
	var result siteTransferSnapshot
	var versionText string
	if err = query.QueryRow(ctx, "SHOW server_version_num").Scan(&versionText); err != nil {
		return siteTransferSnapshot{}, fmt.Errorf("%w: inspect PostgreSQL version", ErrSiteTransfer)
	}
	result.PostgreSQLVersionNum, err = strconv.Atoi(versionText)
	if err != nil || result.PostgreSQLVersionNum/10000 != 18 {
		return siteTransferSnapshot{}, fmt.Errorf("%w: PostgreSQL 18 is required by this transfer profile", ErrSiteTransfer)
	}
	if err = query.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()").Scan(&result.OtherSessions); err != nil {
		return siteTransferSnapshot{}, fmt.Errorf("%w: inspect database sessions", ErrSiteTransfer)
	}
	if err = query.QueryRow(ctx, "SELECT count(*) FROM media.assets WHERE state IN ('pending','deleting')").Scan(&result.Unfinished); err != nil {
		return siteTransferSnapshot{}, fmt.Errorf("%w: inspect Media state", ErrSiteTransfer)
	}
	var enabledAdmin int64
	if err = query.QueryRow(ctx, "SELECT count(*) FROM identity.accounts WHERE id=$1 AND enabled", adminPrincipal).Scan(&enabledAdmin); err != nil || enabledAdmin != 1 {
		return siteTransferSnapshot{}, fmt.Errorf("%w: site administrator account missing or disabled", ErrSiteTransfer)
	}

	result.Ledgers = make(map[string][]SiteTransferLedgerEntry, 4)
	for _, item := range []struct{ name, query string }{
		{"identity", "SELECT version,checksum FROM identity.schema_migrations ORDER BY version"},
		{"audit", "SELECT version,checksum FROM audit.schema_migrations ORDER BY version"},
		{"media", "SELECT version,checksum FROM media.schema_migrations ORDER BY version"},
		{"rixa", "SELECT version,identity FROM rixa.schema_migrations ORDER BY version"},
	} {
		rows, queryErr := query.Query(ctx, item.query)
		if queryErr != nil {
			return siteTransferSnapshot{}, fmt.Errorf("%w: %s migration ledger", ErrSiteTransfer, item.name)
		}
		var ledger []SiteTransferLedgerEntry
		for rows.Next() {
			var entry SiteTransferLedgerEntry
			if scanErr := rows.Scan(&entry.Version, &entry.Identity); scanErr != nil {
				rows.Close()
				return siteTransferSnapshot{}, fmt.Errorf("%w: %s migration ledger", ErrSiteTransfer, item.name)
			}
			ledger = append(ledger, entry)
		}
		if rows.Err() != nil {
			rows.Close()
			return siteTransferSnapshot{}, fmt.Errorf("%w: %s migration ledger", ErrSiteTransfer, item.name)
		}
		rows.Close()
		if len(ledger) == 0 {
			return siteTransferSnapshot{}, fmt.Errorf("%w: empty %s migration ledger", ErrSiteTransfer, item.name)
		}
		result.Ledgers[item.name] = ledger
	}

	summaryQueries := []struct {
		target *int64
		query  string
	}{
		{&result.Summary.Accounts, "SELECT count(*) FROM identity.accounts"},
		{&result.Summary.AuditRecords, "SELECT count(*) FROM audit.records"},
		{&result.Summary.ContentItems, "SELECT count(*) FROM rixa.content_items"},
		{&result.Summary.ContentRevisions, "SELECT count(*) FROM rixa.content_revisions"},
		{&result.Summary.ContentMediaRefs, "SELECT count(*) FROM rixa.content_media_refs"},
		{&result.Summary.EditorialOperations, "SELECT count(*) FROM rixa.operations"},
		{&result.Summary.ReadyMedia, "SELECT count(*) FROM media.assets WHERE state='ready'"},
		{&result.Summary.SourceSessionRows, "SELECT count(*) FROM identity.sessions"},
	}
	for _, item := range summaryQueries {
		if err = query.QueryRow(ctx, item.query).Scan(item.target); err != nil {
			return siteTransferSnapshot{}, fmt.Errorf("%w: summarize site database", ErrSiteTransfer)
		}
	}
	if err = query.QueryRow(ctx, "SELECT r.revision,r.theme FROM rixa.appearance_head h JOIN rixa.appearance_revisions r ON r.revision=h.head_revision WHERE h.singleton").Scan(&result.Summary.AppearanceRevision, &result.Summary.Theme); err != nil {
		return siteTransferSnapshot{}, fmt.Errorf("%w: summarize site appearance", ErrSiteTransfer)
	}

	rows, err := query.Query(ctx, "SELECT id,revision,size,sha256 FROM media.assets WHERE state='ready' ORDER BY id")
	if err != nil {
		return siteTransferSnapshot{}, fmt.Errorf("%w: list ready Media", ErrSiteTransfer)
	}
	for rows.Next() {
		var asset SiteTransferMediaAsset
		if err = rows.Scan(&asset.ID, &asset.Revision, &asset.Size, &asset.SHA256); err != nil {
			rows.Close()
			return siteTransferSnapshot{}, fmt.Errorf("%w: list ready Media", ErrSiteTransfer)
		}
		result.ReadyMedia = append(result.ReadyMedia, asset)
	}
	if rows.Err() != nil {
		rows.Close()
		return siteTransferSnapshot{}, fmt.Errorf("%w: list ready Media", ErrSiteTransfer)
	}
	rows.Close()

	var brokenRefs int64
	if err = query.QueryRow(ctx, "SELECT count(*) FROM rixa.content_media_refs r LEFT JOIN media.assets a ON a.id=r.asset_id AND a.revision=r.asset_revision AND a.state='ready' WHERE a.id IS NULL").Scan(&brokenRefs); err != nil || brokenRefs != 0 {
		return siteTransferSnapshot{}, fmt.Errorf("%w: content/Media references are not coherent", ErrSiteTransfer)
	}
	return result, nil
}

func equalSourceTransferSnapshots(a, b siteTransferSnapshot) bool {
	a.OtherSessions, b.OtherSessions = 0, 0
	return reflect.DeepEqual(a, b)
}

func equalRestoredSiteTransferSnapshot(manifest SiteTransferManifest, got siteTransferSnapshot) bool {
	if got.PostgreSQLVersionNum/10000 != manifest.PostgreSQLVersionNum/10000 ||
		!reflect.DeepEqual(manifest.Ledgers, got.Ledgers) ||
		!reflect.DeepEqual(manifest.ReadyMedia, got.ReadyMedia) {
		return false
	}
	want := manifest.Summary
	want.SourceSessionRows = 0
	return reflect.DeepEqual(want, got.Summary)
}

func runSiteTransferDump(parent context.Context, tool string, env []string, output string) (SiteTransferArtifact, error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return SiteTransferArtifact{}, fmt.Errorf("%w: create database dump", ErrSiteTransfer)
	}
	writer := &boundedTransferWriter{writer: file, hash: sha256.New(), max: maxSiteTransferDumpBytes}
	cmd := exec.CommandContext(ctx, tool,
		"--format=custom",
		"--no-owner",
		"--no-privileges",
		"--exclude-table-data=identity.sessions",
	)
	cmd.Env = env
	cmd.Stdout = writer
	cmd.Stderr = io.Discard
	runErr := cmd.Run()
	syncErr := file.Sync()
	closeErr := file.Close()
	if runErr != nil || syncErr != nil || closeErr != nil || writer.err != nil || writer.count < 1 {
		_ = os.Remove(output)
		if writer.err != nil {
			return SiteTransferArtifact{}, writer.err
		}
		return SiteTransferArtifact{}, fmt.Errorf("%w: pg_dump failed", ErrSiteTransfer)
	}
	return SiteTransferArtifact{
		File: filepath.Base(output), Size: writer.count, SHA256: hex.EncodeToString(writer.hash.Sum(nil)),
	}, nil
}

func siteTransferToolVersion(parent context.Context, tool string, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, "--version")
	cmd.Env = env
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil || output.Len() == 0 || output.Len() > 4096 {
		return "", fmt.Errorf("%w: PostgreSQL tool version", ErrSiteTransfer)
	}
	return strings.TrimSpace(output.String()), nil
}

func siteTransferPostgres18Tool(version string) bool {
	fields := strings.Fields(version)
	for _, field := range fields {
		if strings.HasPrefix(field, "18.") || field == "18" {
			return true
		}
	}
	return false
}

func runSiteTransferRestore(parent context.Context, tool string, env []string, databaseName, input string) error {
	file, err := os.Open(input)
	if err != nil {
		return fmt.Errorf("%w: open database dump", ErrSiteTransfer)
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool,
		"--exit-on-error",
		"--single-transaction",
		"--no-owner",
		"--no-privileges",
		"--dbname", databaseName,
	)
	cmd.Env = env
	cmd.Stdin = file
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("%w: pg_restore failed", ErrSiteTransfer)
	}
	return nil
}

type boundedTransferWriter struct {
	writer io.Writer
	hash   hashWriter
	max    int64
	count  int64
	err    error
}

type hashWriter interface {
	io.Writer
	Sum([]byte) []byte
}

func (w *boundedTransferWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if int64(len(p)) > w.max-w.count {
		w.err = ErrSiteTransferLimited
		return 0, w.err
	}
	n, err := w.writer.Write(p)
	if n > 0 {
		_, _ = w.hash.Write(p[:n])
		w.count += int64(n)
	}
	if err != nil {
		w.err = err
	}
	return n, err
}

func ensureEmptySiteTransferDatabase(parent context.Context, dsn string, sourceVersionNum int) error {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg == nil || !siteTransferLocalHost(cfg.Host) || len(cfg.Fallbacks) != 0 || cfg.TLSConfig != nil {
		return fmt.Errorf("%w: target database profile", ErrSiteTransfer)
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("%w: inspect target database", ErrSiteTransfer)
	}
	defer conn.Close(context.Background())
	var versionText string
	if err = conn.QueryRow(ctx, "SHOW server_version_num").Scan(&versionText); err != nil {
		return fmt.Errorf("%w: target PostgreSQL version", ErrSiteTransfer)
	}
	targetVersion, versionErr := strconv.Atoi(versionText)
	if versionErr != nil || targetVersion/10000 != 18 || targetVersion/10000 != sourceVersionNum/10000 {
		return fmt.Errorf("%w: incompatible target PostgreSQL major", ErrSiteTransfer)
	}
	var others int64
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()").Scan(&others); err != nil || others != 0 {
		return fmt.Errorf("%w: target database has other sessions", ErrSiteTransfer)
	}
	var userRelations int64
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND n.nspname NOT LIKE 'pg_temp_%'").Scan(&userRelations); err != nil || userRelations != 0 {
		return fmt.Errorf("%w: target database contains user relations", ErrSiteTransfer)
	}
	var customSchemas int64
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM pg_namespace WHERE nspname NOT IN ('pg_catalog','information_schema','public') AND nspname NOT LIKE 'pg_toast%' AND nspname NOT LIKE 'pg_temp_%'").Scan(&customSchemas); err != nil || customSchemas != 0 {
		return fmt.Errorf("%w: target database contains custom schemas", ErrSiteTransfer)
	}
	for _, schema := range []string{"identity", "audit", "media", "rixa"} {
		var exists bool
		if err = conn.QueryRow(ctx, "SELECT to_regnamespace($1) IS NOT NULL", schema).Scan(&exists); err != nil || exists {
			return fmt.Errorf("%w: target database is not empty", ErrSiteTransfer)
		}
	}
	return nil
}

func verifyMediaTransferEntries(assets []SiteTransferMediaAsset, entries []SiteTransferArchiveEntry) error {
	if len(assets) != len(entries) {
		return fmt.Errorf("%w: Media file count does not match ready metadata", ErrSiteTransfer)
	}
	expected := make(map[string]SiteTransferMediaAsset, len(assets))
	for _, asset := range assets {
		expected[asset.ID] = asset
	}
	for _, entry := range entries {
		if entry.Type != "file" || strings.Contains(entry.Path, "/") || !accountIDSyntax.MatchString(entry.Path) ||
			entry.Mode != 0o600 {
			return fmt.Errorf("%w: Media root contains unsupported entry", ErrSiteTransfer)
		}
		asset, ok := expected[entry.Path]
		if !ok || asset.Size != entry.Size || asset.SHA256 != entry.SHA256 {
			return fmt.Errorf("%w: Media file/metadata mismatch", ErrSiteTransfer)
		}
		delete(expected, entry.Path)
	}
	if len(expected) != 0 {
		return fmt.Errorf("%w: missing Media originals", ErrSiteTransfer)
	}
	return nil
}

func writeSiteTransferMetadata(root string, manifest SiteTransferManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil || int64(len(data)) > maxSiteTransferManifestBytes {
		return fmt.Errorf("%w: encode transfer manifest", ErrSiteTransfer)
	}
	data = append(data, '\n')
	manifestPath := filepath.Join(root, siteTransferManifestFile)
	if err = writePrivateTransferFile(manifestPath, data); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	completeData, err := json.Marshal(siteTransferComplete{ManifestSHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		return fmt.Errorf("%w: encode transfer completion marker", ErrSiteTransfer)
	}
	completeData = append(completeData, '\n')
	if err = writePrivateTransferFile(filepath.Join(root, siteTransferCompleteFile), completeData); err != nil {
		return err
	}
	return syncTransferDirectory(root)
}

func readSiteTransferMetadata(root string) (SiteTransferManifest, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || info.Mode()&os.ModeSymlink != 0 {
		return SiteTransferManifest{}, fmt.Errorf("%w: transfer source directory", ErrSiteTransfer)
	}
	manifestData, err := readBoundedTransferFile(filepath.Join(root, siteTransferManifestFile), maxSiteTransferManifestBytes)
	if err != nil {
		return SiteTransferManifest{}, err
	}
	completeData, err := readBoundedTransferFile(filepath.Join(root, siteTransferCompleteFile), 4096)
	if err != nil {
		return SiteTransferManifest{}, err
	}
	var complete siteTransferComplete
	if err = decodeStrictTransferJSON(completeData, &complete); err != nil {
		return SiteTransferManifest{}, err
	}
	sum := sha256.Sum256(manifestData)
	if complete.ManifestSHA256 != hex.EncodeToString(sum[:]) {
		return SiteTransferManifest{}, fmt.Errorf("%w: incomplete/tampered transfer manifest", ErrSiteTransfer)
	}
	var manifest SiteTransferManifest
	if err = decodeStrictTransferJSON(manifestData, &manifest); err != nil {
		return SiteTransferManifest{}, err
	}
	if err = validateSiteTransferManifest(manifest); err != nil {
		return SiteTransferManifest{}, err
	}
	return manifest, nil
}

func decodeStrictTransferJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("%w: decode transfer metadata", ErrSiteTransfer)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing transfer metadata", ErrSiteTransfer)
	}
	return nil
}

func readBoundedTransferFile(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() < 1 || info.Size() > max {
		return nil, fmt.Errorf("%w: transfer metadata file", ErrSiteTransfer)
	}
	data, err := os.ReadFile(path)
	if err != nil || int64(len(data)) != info.Size() {
		return nil, fmt.Errorf("%w: read transfer metadata", ErrSiteTransfer)
	}
	return data, nil
}

func writePrivateTransferFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("%w: create transfer metadata", ErrSiteTransfer)
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return fmt.Errorf("%w: persist transfer metadata", ErrSiteTransfer)
	}
	return nil
}

func verifySiteTransferArtifact(root string, artifact SiteTransferArtifact, max int64) error {
	path := filepath.Join(root, artifact.File)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		info.Size() != artifact.Size || info.Size() > max {
		return fmt.Errorf("%w: transfer artifact metadata mismatch", ErrSiteTransfer)
	}
	hash, size, err := hashTransferFile(path, max)
	if err != nil || size != artifact.Size || hash != artifact.SHA256 {
		return fmt.Errorf("%w: transfer artifact integrity mismatch", ErrSiteTransfer)
	}
	return nil
}

func hashTransferFile(path string, max int64) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(file, max+1))
	if err != nil || count > max {
		return "", count, ErrSiteTransferLimited
	}
	return hex.EncodeToString(hash.Sum(nil)), count, nil
}

func ensureNewTransferRootTarget(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return fmt.Errorf("%w: restore root path", ErrSiteTransfer)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: restore root already exists", ErrSiteTransfer)
	}
	parent := filepath.Dir(path)
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil || canonical != parent {
		return fmt.Errorf("%w: restore root parent", ErrSiteTransfer)
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: restore root parent", ErrSiteTransfer)
	}
	return nil
}

func newTransferStage(target string) (string, error) {
	parent := filepath.Dir(target)
	stage, err := os.MkdirTemp(parent, ".rixa-site-transfer-")
	if err != nil {
		return "", fmt.Errorf("%w: create restore staging root", ErrSiteTransfer)
	}
	if err = os.Chmod(stage, 0o700); err != nil {
		_ = os.RemoveAll(stage)
		return "", fmt.Errorf("%w: secure restore staging root", ErrSiteTransfer)
	}
	return stage, nil
}

func activateSiteTransferRoots(mediaStage, mediaTarget, publicStage, publicTarget string) error {
	if mediaStage == "" {
		return fmt.Errorf("%w: missing Media staging root", ErrSiteTransfer)
	}
	if err := os.Rename(mediaStage, mediaTarget); err != nil {
		return fmt.Errorf("%w: activate Media root", ErrSiteTransfer)
	}
	mediaActivated := true
	if publicStage != "" {
		if err := os.Rename(publicStage, publicTarget); err != nil {
			if rollbackErr := os.Rename(mediaTarget, mediaStage); rollbackErr != nil {
				return fmt.Errorf("%w: partial root activation", ErrSiteTransferUnknown)
			}
			if rollbackSyncErr := syncTransferDirectory(filepath.Dir(mediaTarget)); rollbackSyncErr != nil {
				return fmt.Errorf("%w: rollback root activation durability", ErrSiteTransferUnknown)
			}
			mediaActivated = false
			return fmt.Errorf("%w: activate public root", ErrSiteTransfer)
		}
	}
	if mediaActivated {
		if err := syncTransferDirectory(filepath.Dir(mediaTarget)); err != nil {
			return fmt.Errorf("%w: root activation durability", ErrSiteTransferUnknown)
		}
	}
	if publicTarget != "" && filepath.Dir(publicTarget) != filepath.Dir(mediaTarget) {
		if err := syncTransferDirectory(filepath.Dir(publicTarget)); err != nil {
			return fmt.Errorf("%w: root activation durability", ErrSiteTransferUnknown)
		}
	}
	return nil
}

func syncTransferDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: open directory for sync", ErrSiteTransfer)
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return fmt.Errorf("%w: sync directory", ErrSiteTransfer)
	}
	return nil
}
