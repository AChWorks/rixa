// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/media"
)

const (
	PublicationTarget          = "publication"
	publicationManifestVersion = 1
	maxPublicationAssets          = 64
	maxPublicationEntries         = 512
	maxPublicationAliasesPerEntry = 16
	maxPublicationBytes     int64  = 256 << 20
	publicationHistoryLimit       = 16
	publicReadConcurrency      = 32
	publicationApplyTimeout   = 2 * time.Minute
)

var publicRouteSegmentSyntax = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$`)

type PublicationRequest struct {
	OperationID        string
	ExpectedGeneration string
	ContentID          string
	Route              string
}

type PublicationResult struct {
	Generation string
	ContentID  string
	Route      string
	CreatedAt  time.Time
}

type PublicationOperation struct {
	ID          string    `json:"id"`
	RequestHash string    `json:"request_hash"`
	Generation  string    `json:"generation"`
	ContentID   string    `json:"content_id,omitempty"`
	Route       string    `json:"route,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type PublicationState struct {
	Enabled          bool
	Generation       string
	DesiredRoute     string
	CanonicalRoute   string
	ActiveRevision   int64
	FirstPublishedAt time.Time
	ModifiedAt       time.Time
}

type publicationAsset struct {
	SourceAssetID  string `json:"source_asset_id"`
	SourceRevision int64  `json:"source_revision"`
	SourceSHA256   string `json:"source_sha256"`
	Profile        string `json:"profile"`
	MIME           string `json:"mime"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	PublicPath     string `json:"public_path"`
	File           string `json:"file"`
}

type publicationEntry struct {
	ContentID        string      `json:"content_id"`
	Kind             ContentKind `json:"kind"`
	DesiredRoute     string      `json:"desired_route"`
	CanonicalRoute   string      `json:"canonical_route,omitempty"`
	Aliases          []string    `json:"aliases,omitempty"`
	Active           bool        `json:"active"`
	Revision         int64       `json:"revision,omitempty"`
	FirstPublishedAt time.Time   `json:"first_published_at,omitempty"`
	ModifiedAt       time.Time   `json:"modified_at,omitempty"`
	SourceKey        string      `json:"source_key,omitempty"`
	File             string      `json:"file,omitempty"`
}

type publicationRoute struct {
	Kind      string `json:"kind"`
	ContentID string `json:"content_id,omitempty"`
	File      string `json:"file,omitempty"`
	Target    string `json:"target,omitempty"`
}

type publicationFile struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Cache       string `json:"cache"`
	Robots      string `json:"robots,omitempty"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
}

type publicationManifest struct {
	Version           int                         `json:"version"`
	SiteID            string                      `json:"site_id"`
	Origin            string                      `json:"origin"`
	Generation        string                      `json:"generation"`
	ParentGeneration  string                      `json:"parent_generation,omitempty"`
	CreatedAt         time.Time                   `json:"created_at"`
	SourceFingerprint string                      `json:"source_fingerprint"`
	AppearanceRevision int64                      `json:"appearance_revision"`
	Policy            PublicPolicyConfig          `json:"policy"`
	Operation         PublicationOperation        `json:"operation"`
	Entries           map[string]publicationEntry `json:"entries"`
	Routes            map[string]publicationRoute `json:"routes"`
	Assets            map[string]publicationAsset `json:"assets"`
	Files             map[string]publicationFile  `json:"files"`
}

type publishedGeneration struct {
	manifest *publicationManifest
	root     string
	readers  atomic.Int64
	retired  atomic.Bool
}

type publicationReadState struct {
	generations []*publishedGeneration
	assets      map[string]*publishedGeneration
}

type PublicationService struct {
	siteID    string
	origin    string
	authority string
	root      string
	policy    PublicPolicyConfig
	app       *achrix.Application
	editorial *EditorialService
	media     *media.Service
	slots     chan struct{}
	applyGate chan struct{}

	mu     sync.Mutex
	readMu sync.Mutex
	ready           atomic.Bool
	state           atomic.Pointer[publicationReadState]
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc
}

func newPublicationService(site ResolvedSite, app *achrix.Application, editorial *EditorialService, mediaService *media.Service) (*PublicationService, error) {
	if site.PublicRoot == "" {
		return nil, nil
	}
	if app == nil || editorial == nil || mediaService == nil {
		return nil, ErrConfiguration
	}
	if err := site.PublicPolicy.validate(); err != nil {
		return nil, ErrConfiguration
	}
	authority, err := originAuthority(site.Origin)
	if err != nil {
		return nil, ErrConfiguration
	}
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	return &PublicationService{
		siteID: site.ID, origin: site.Origin, authority: authority,
		root: site.PublicRoot, policy: site.PublicPolicy,
		app: app, editorial: editorial, media: mediaService,
		slots: make(chan struct{}, publicReadConcurrency),
		applyGate: make(chan struct{}, 1),
		lifecycleCtx: lifecycleCtx, lifecycleCancel: lifecycleCancel,
	}, nil
}

func (s *PublicationService) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.ready.Store(false)
	if s.lifecycleCancel != nil {
		s.lifecycleCancel()
	}
	if s.applyGate == nil {
		return nil
	}
	select {
	case s.applyGate <- struct{}{}:
		<-s.applyGate
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *PublicationService) State(contentID string, kind ContentKind) PublicationState {
	if s == nil {
		return PublicationState{}
	}
	result := PublicationState{Enabled: true}
	current := s.state.Load()
	if current == nil || len(current.generations) == 0 {
		if validEditorialID(contentID) && validContentKind(kind) {
			result.DesiredRoute = defaultPublicationRoute(kind, contentID)
		}
		return result
	}
	manifest := current.generations[0].manifest
	result.Generation = manifest.Generation
	if entry, ok := manifest.Entries[contentID]; ok {
		result.DesiredRoute = entry.DesiredRoute
		result.CanonicalRoute = entry.CanonicalRoute
		if entry.Active {
			result.ActiveRevision = entry.Revision
		}
		result.FirstPublishedAt = entry.FirstPublishedAt
		result.ModifiedAt = entry.ModifiedAt
	} else if validEditorialID(contentID) && validContentKind(kind) {
		result.DesiredRoute = defaultPublicationRoute(kind, contentID)
	}
	return result
}

func (s *PublicationService) Operation(ctx context.Context, actor achrix.Principal, operationID string) (PublicationOperation, error) {
	if s == nil || !validOperationID(operationID) {
		return PublicationOperation{}, ErrEditorialInvalid
	}
	if err := s.editorial.authorize(ctx, actor, CapabilityOperationRead, OperationCollectionTarget); err != nil {
		return PublicationOperation{}, err
	}
	if operation, ok := s.findOperation(operationID); ok {
		return operation, nil
	}
	return PublicationOperation{}, ErrEditorialNotFound
}

func (s *PublicationService) Apply(ctx context.Context, actor achrix.Principal, request PublicationRequest) (PublicationResult, error) {
	if s == nil || !validOperationID(request.OperationID) {
		return PublicationResult{}, ErrEditorialInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, publicationApplyTimeout)
	defer cancel()
	if s.lifecycleCtx == nil {
		return PublicationResult{}, ErrEditorialUnavailable
	}
	stopLifecycleCancel := context.AfterFunc(s.lifecycleCtx, cancel)
	defer stopLifecycleCancel()
	if request.ExpectedGeneration != "" && !validEditorialID(request.ExpectedGeneration) {
		return PublicationResult{}, ErrEditorialInvalid
	}
	if request.ContentID != "" && !validEditorialID(request.ContentID) {
		return PublicationResult{}, ErrEditorialInvalid
	}
	if request.ContentID == "" && request.Route != "" {
		return PublicationResult{}, ErrEditorialInvalid
	}
	if request.Route != "" {
		if _, err := normalizePublicationRoute(request.Route); err != nil {
			return PublicationResult{}, err
		}
	}
	if err := s.editorial.authorize(ctx, actor, CapabilityPublicationApply, PublicationTarget); err != nil {
		return PublicationResult{}, err
	}

	requestHash := operationRequestHash(
		"publication.apply", string(actor), request.ExpectedGeneration, request.ContentID, request.Route,
	)

	select {
	case s.applyGate <- struct{}{}:
		defer func() { <-s.applyGate }()
	case <-ctx.Done():
		return PublicationResult{}, ctx.Err()
	}
	releaseSource, err := s.editorial.acquirePublicationSource(ctx)
	if err != nil {
		return PublicationResult{}, err
	}
	defer releaseSource()

	if !s.ready.Load() {
		return PublicationResult{}, ErrEditorialUnavailable
	}
	if operation, ok := s.findOperation(request.OperationID); ok {
		if operation.RequestHash != requestHash {
			return PublicationResult{}, ErrEditorialConflict
		}
		return PublicationResult{
			Generation: operation.Generation, ContentID: operation.ContentID,
			Route: operation.Route, CreatedAt: operation.CreatedAt,
		}, nil
	}

	current := s.state.Load()
	currentGeneration := ""
	var previous *publicationManifest
	if current != nil && len(current.generations) != 0 {
		previous = current.generations[0].manifest
		currentGeneration = previous.Generation
	}
	if request.ExpectedGeneration != currentGeneration {
		return PublicationResult{}, ErrEditorialConflict
	}

	snapshot, err := s.editorial.store.publicationSnapshot(ctx)
	if err != nil {
		return PublicationResult{}, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if previous != nil && !now.After(previous.CreatedAt) {
		now = previous.CreatedAt.Add(time.Microsecond)
	}
	generation := randText()
	if !validEditorialID(generation) {
		return PublicationResult{}, ErrEditorialUnavailable
	}
	stage := filepath.Join(s.root, ".stage-"+generation)
	if err = os.Mkdir(stage, 0o700); err != nil {
		return PublicationResult{}, ErrEditorialUnavailable
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(stage)
		}
	}()
	publicDir := filepath.Join(stage, "public")
	if err = os.Mkdir(publicDir, 0o700); err != nil {
		return PublicationResult{}, ErrEditorialUnavailable
	}

	entries, routes, err := buildPublicationLayout(snapshot, previous, request, now)
	if err != nil {
		return PublicationResult{}, err
	}
	assets, assetBytes, err := s.prepareAssets(ctx, actor, snapshot, publicDir)
	if err != nil {
		return PublicationResult{}, err
	}
	if assetBytes > maxPublicationBytes {
		return PublicationResult{}, ErrEditorialLimited
	}

	manifest := &publicationManifest{
		Version: publicationManifestVersion, SiteID: s.siteID, Origin: s.origin,
		Generation: generation, CreatedAt: now, SourceFingerprint: snapshot.Fingerprint,
		AppearanceRevision: snapshot.Appearance.Revision, Policy: s.policy,
		Entries: entries, Routes: routes, Assets: assets,
		Files: make(map[string]publicationFile),
	}
	if previous != nil {
		manifest.ParentGeneration = previous.Generation
	}
	manifest.Operation = PublicationOperation{
		ID: request.OperationID, RequestHash: requestHash, Generation: generation,
		ContentID: request.ContentID, CreatedAt: now,
	}

	if request.ContentID != "" {
		if entry, ok := entries[request.ContentID]; ok {
			manifest.Operation.Route = entry.DesiredRoute
		}
	}

	renderedBytes, err := renderPublication(publicDir, manifest, snapshot, assets)
	if err != nil {
		return PublicationResult{}, err
	}
	if assetBytes+renderedBytes > maxPublicationBytes {
		return PublicationResult{}, ErrEditorialLimited
	}

	latest, err := s.editorial.store.publicationSnapshot(ctx)
	if err != nil {
		return PublicationResult{}, err
	}
	if latest.Fingerprint != snapshot.Fingerprint {
		return PublicationResult{}, ErrEditorialConflict
	}
	if err = s.verifyAssets(ctx, actor, assets); err != nil {
		return PublicationResult{}, err
	}

	if err = writePublicationManifest(stage, manifest); err != nil {
		return PublicationResult{}, err
	}
	if err = syncDir(publicDir); err != nil {
		return PublicationResult{}, ErrEditorialUnavailable
	}
	if err = syncDir(stage); err != nil {
		return PublicationResult{}, ErrEditorialUnavailable
	}

	generations := filepath.Join(s.root, "generations")
	if err = os.MkdirAll(generations, 0o700); err != nil {
		return PublicationResult{}, ErrEditorialUnavailable
	}
	finalDir := filepath.Join(generations, generation)
	if err = os.Rename(stage, finalDir); err != nil {
		return PublicationResult{}, ErrEditorialUnavailable
	}
	cleanup = false
	if err = syncDir(generations); err != nil {
		_ = os.RemoveAll(finalDir)
		return PublicationResult{}, ErrEditorialUnavailable
	}

	gen := &publishedGeneration{manifest: manifest, root: finalDir}
	pointerCommitted, pointerErr := s.activateGeneration(generation)
	var retired []*publishedGeneration
	if pointerCommitted {
		retired = s.installGeneration(gen)
	}
	if pointerErr != nil {
		if pointerCommitted {
			return PublicationResult{}, ErrEditorialUnknownOutcome
		}
		_ = os.RemoveAll(finalDir)
		return PublicationResult{}, ErrEditorialUnavailable
	}

	s.cleanupRetired(retired)
	return PublicationResult{
		Generation: generation, ContentID: request.ContentID,
		Route: manifest.Operation.Route, CreatedAt: now,
	}, nil
}

func buildPublicationLayout(snapshot publicationSnapshot, previous *publicationManifest, request PublicationRequest, now time.Time) (map[string]publicationEntry, map[string]publicationRoute, error) {
	entries := make(map[string]publicationEntry)
	if previous != nil {
		for id, entry := range previous.Entries {
			copyEntry := entry
			copyEntry.Aliases = append([]string(nil), entry.Aliases...)
			copyEntry.Active = false
			copyEntry.CanonicalRoute = ""
			copyEntry.Revision = 0
			copyEntry.File = ""
			entries[id] = copyEntry
		}
	}

	if len(entries) > maxPublicationEntries {
		return nil, nil, ErrEditorialLimited
	}

	active := make(map[string]ContentRevision, len(snapshot.Contents))
	for _, content := range snapshot.Contents {
		active[content.ID] = content
		entry, exists := entries[content.ID]
		if !exists {
			entry = publicationEntry{
				ContentID: content.ID, Kind: content.Kind,
				DesiredRoute: defaultPublicationRoute(content.Kind, content.ID),
			}
		}
		entry.ContentID = content.ID
		entry.Kind = content.Kind
		entry.Active = true
		entry.Revision = content.Revision
		entry.File = "content/" + strings.ToLower(content.ID) + ".html"
		if entry.FirstPublishedAt.IsZero() {
			entry.FirstPublishedAt = now
		}
		entries[content.ID] = entry
		if len(entries) > maxPublicationEntries {
			return nil, nil, ErrEditorialLimited
		}
	}

	for id, entry := range entries {
		if !entry.Active {
			entry.SourceKey = ""
			entries[id] = entry
		}
	}

	if request.ContentID != "" {
		previouslyOwned := false
		if previous != nil {
			_, previouslyOwned = previous.Entries[request.ContentID]
		}
		entry, exists := entries[request.ContentID]
		if !exists {
			return nil, nil, ErrEditorialInvalid
		}
		if !entry.Active {
			if request.Route != "" && request.Route != entry.DesiredRoute {
				return nil, nil, ErrEditorialConflict
			}
		} else if request.Route != "" {
			normalized, err := normalizePublicationRoute(request.Route)
			if err != nil {
				return nil, nil, err
			}
			if normalized != entry.DesiredRoute {
				oldRoute := entry.DesiredRoute
				entry.DesiredRoute = normalized
				entry.Aliases = removeRoute(entry.Aliases, normalized)
				if previouslyOwned && oldRoute != "" && oldRoute != normalized {
					entry.Aliases = appendUniqueRoute(entry.Aliases, oldRoute)
				}
			}
			entries[request.ContentID] = entry
		}
	}

	homeID := ""
	if snapshot.Appearance.HomeMode == "page" {
		content, ok := active[snapshot.Appearance.HomePageID]
		if !ok || content.Kind != ContentPage {
			return nil, nil, ErrEditorialConflict
		}
		homeID = content.ID
	}

	owners := make(map[string]string)
	for id, entry := range entries {
		if entry.DesiredRoute == "" {
			return nil, nil, ErrEditorialConflict
		}
		route, err := normalizePublicationRoute(entry.DesiredRoute)
		if err != nil {
			return nil, nil, err
		}
		entry.DesiredRoute = route
		entry.Aliases = normalizeAliasSet(entry.Aliases, route)
		if len(entry.Aliases) > maxPublicationAliasesPerEntry {
			return nil, nil, ErrEditorialLimited
		}
		for _, owned := range append([]string{entry.DesiredRoute}, entry.Aliases...) {
			if previousOwner, exists := owners[owned]; exists && previousOwner != id {
				return nil, nil, ErrEditorialConflict
			}
			owners[owned] = id
		}
		if entry.Active {
			entry.CanonicalRoute = entry.DesiredRoute
			if id == homeID {
				entry.CanonicalRoute = "/"
			}
		}
		entries[id] = entry
	}

	routes := make(map[string]publicationRoute)
	if snapshot.Appearance.HomeMode == "latest_posts" {
		routes["/"] = publicationRoute{Kind: "file", File: "home.html"}
	} else {
		home := entries[homeID]
		routes["/"] = publicationRoute{Kind: "file", ContentID: homeID, File: home.File}
	}

	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		entry := entries[id]
		owned := append([]string{entry.DesiredRoute}, entry.Aliases...)
		if !entry.Active {
			for _, route := range owned {
				routes[route] = publicationRoute{Kind: "gone", ContentID: id}
			}
			continue
		}
		if entry.CanonicalRoute != "/" {
			routes[entry.CanonicalRoute] = publicationRoute{Kind: "file", ContentID: id, File: entry.File}
		}
		for _, route := range owned {
			if route == entry.CanonicalRoute {
				continue
			}
			routes[route] = publicationRoute{Kind: "redirect", ContentID: id, Target: entry.CanonicalRoute}
		}
	}
	routes["/sitemap.xml"] = publicationRoute{Kind: "file", File: "sitemap.xml"}
	routes["/robots.txt"] = publicationRoute{Kind: "file", File: "robots.txt"}
	return entries, routes, nil
}

func defaultPublicationRoute(kind ContentKind, id string) string {
	prefix := "posts"
	if kind == ContentPage {
		prefix = "pages"
	}
	return "/" + prefix + "/" + strings.ToLower(id) + "/"
}

func appendUniqueRoute(routes []string, value string) []string {
	for _, route := range routes {
		if route == value {
			return routes
		}
	}
	return append(routes, value)
}

func removeRoute(routes []string, value string) []string {
	result := routes[:0]
	for _, route := range routes {
		if route != value {
			result = append(result, route)
		}
	}
	return result
}

func normalizeAliasSet(routes []string, desired string) []string {
	seen := make(map[string]struct{}, len(routes))
	result := make([]string, 0, len(routes))
	for _, route := range routes {
		if route == "" || route == "/" || route == desired {
			continue
		}
		normalized, err := normalizePublicationRoute(route)
		if err != nil {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	sort.Strings(result)
	return result
}

func normalizePublicationRoute(raw string) (string, error) {
	if raw == "" || len(raw) > 512 || raw[0] != '/' || raw == "/" ||
		strings.TrimSpace(raw) != raw || strings.Contains(raw, "//") ||
		strings.ContainsAny(raw, "\\?#%") {
		return "", ErrEditorialInvalid
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f || forbiddenEditorialDirectionControl(r) {
			return "", ErrEditorialInvalid
		}
	}
	if !strings.HasSuffix(raw, "/") {
		raw += "/"
	}
	segments := strings.Split(strings.Trim(raw, "/"), "/")
	for _, segment := range segments {
		if !publicRouteSegmentSyntax.MatchString(segment) || segment == "." || segment == ".." {
			return "", ErrEditorialInvalid
		}
	}
	for _, reserved := range []string{"/admin/", "/auth/", "/assets/"} {
		if strings.HasPrefix(raw, reserved) {
			return "", ErrEditorialInvalid
		}
	}
	if raw == "/sitemap.xml/" || raw == "/robots.txt/" {
		return "", ErrEditorialInvalid
	}
	return raw, nil
}

func (s *PublicationService) prepareAssets(ctx context.Context, actor achrix.Principal, snapshot publicationSnapshot, publicDir string) (map[string]publicationAsset, int64, error) {
	refs := make(map[string]int64)
	for _, content := range snapshot.Contents {
		for _, ref := range content.Media {
			if prior, exists := refs[ref.AssetID]; exists && prior != ref.AssetRevision {
				return nil, 0, ErrEditorialConflict
			}
			refs[ref.AssetID] = ref.AssetRevision
		}
	}
	if len(refs) > maxPublicationAssets {
		return nil, 0, ErrEditorialLimited
	}
	result := make(map[string]publicationAsset, len(refs))
	if len(refs) == 0 {
		return result, 0, nil
	}
	assetDir := filepath.Join(publicDir, "assets")
	if err := os.Mkdir(assetDir, 0o700); err != nil {
		return nil, 0, ErrEditorialUnavailable
	}
	ids := make([]string, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var total int64
	for _, id := range ids {
		revision := refs[id]
		temp := filepath.Join(assetDir, "."+strings.ToLower(id)+".tmp")
		file, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, 0, ErrEditorialUnavailable
		}
		prepared, prepErr := s.media.PreparePublicImage(ctx, actor, media.PreparePublicImageRequest{
			AssetID: id, ExpectedRevision: revision,
		}, file)
		syncErr := file.Sync()
		closeErr := file.Close()
		if prepErr != nil {
			_ = os.Remove(temp)
			return nil, 0, publicationMediaError(prepErr)
		}
		if syncErr != nil || closeErr != nil {
			_ = os.Remove(temp)
			return nil, 0, ErrEditorialUnavailable
		}
		if prepared.SourceAssetID != id || prepared.SourceRevision != revision ||
			prepared.Profile != media.PublicImageProfile || prepared.Size < 1 ||
			prepared.Size > media.MaxPublicImageBytes || prepared.Width < 1 || prepared.Height < 1 ||
			!validSHA256(prepared.SHA256) || !validSHA256(prepared.SourceSHA256) {
			_ = os.Remove(temp)
			return nil, 0, ErrEditorialConflict
		}
		info, statErr := os.Lstat(temp)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
			info.Size() != prepared.Size || verifyFileSHA256(temp, prepared.SHA256) != nil {
			_ = os.Remove(temp)
			return nil, 0, ErrEditorialUnavailable
		}
		ext := ""
		switch prepared.MIME {
		case "image/png":
			ext = ".png"
		case "image/jpeg":
			ext = ".jpg"
		default:
			_ = os.Remove(temp)
			return nil, 0, ErrEditorialInvalid
		}
		publicPath := "/assets/" + prepared.SHA256 + ext
		relative := strings.TrimPrefix(publicPath, "/")
		final := filepath.Join(publicDir, filepath.FromSlash(relative))
		if _, statErr := os.Stat(final); statErr == nil {
			_ = os.Remove(temp)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			_ = os.Remove(temp)
			return nil, 0, ErrEditorialUnavailable
		} else if err = os.Rename(temp, final); err != nil {
			_ = os.Remove(temp)
			return nil, 0, ErrEditorialUnavailable
		}
		total += prepared.Size
		if total > maxPublicationBytes {
			return nil, 0, ErrEditorialLimited
		}
		result[id] = publicationAsset{
			SourceAssetID: prepared.SourceAssetID, SourceRevision: prepared.SourceRevision,
			SourceSHA256: prepared.SourceSHA256, Profile: prepared.Profile, MIME: prepared.MIME,
			Size: prepared.Size, SHA256: prepared.SHA256, Width: prepared.Width, Height: prepared.Height,
			PublicPath: publicPath, File: relative,
		}
	}
	if err := syncDir(assetDir); err != nil {
		return nil, 0, ErrEditorialUnavailable
	}
	return result, total, nil
}

func (s *PublicationService) verifyAssets(ctx context.Context, actor achrix.Principal, assets map[string]publicationAsset) error {
	for id, prepared := range assets {
		asset, err := s.media.Status(ctx, actor, id)
		if err != nil {
			return publicationMediaError(err)
		}
		if asset.State != "ready" || asset.Revision != prepared.SourceRevision || asset.SHA256 != prepared.SourceSHA256 {
			return ErrEditorialConflict
		}
	}
	return nil
}

func publicationMediaError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, achrix.ErrDenied):
		return achrix.ErrDenied
	case errors.Is(err, media.ErrConflict):
		return ErrEditorialConflict
	case errors.Is(err, media.ErrLimited):
		return ErrEditorialLimited
	case errors.Is(err, media.ErrInput), errors.Is(err, media.ErrNotFound):
		return ErrEditorialInvalid
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return ErrEditorialUnavailable
	}
}

func (s *PublicationService) findOperation(operationID string) (PublicationOperation, bool) {
	current := s.state.Load()
	if current == nil {
		return PublicationOperation{}, false
	}
	for _, generation := range current.generations {
		if generation.manifest.Operation.ID == operationID {
			return generation.manifest.Operation, true
		}
	}
	return PublicationOperation{}, false
}

func (s *PublicationService) installGeneration(generation *publishedGeneration) []*publishedGeneration {
	s.readMu.Lock()
	defer s.readMu.Unlock()

	old := s.state.Load()
	generations := []*publishedGeneration{generation}
	var retired []*publishedGeneration
	if old != nil && len(old.generations) != 0 &&
		old.generations[0].manifest.Generation == generation.manifest.ParentGeneration {
		for _, existing := range old.generations {
			if len(generations) < publicationHistoryLimit {
				generations = append(generations, existing)
			} else {
				retired = append(retired, existing)
			}
		}
	} else if old != nil {
		retired = append(retired, old.generations...)
	}
	s.state.Store(newPublicationReadState(generations))
	return retired
}

func newPublicationReadState(generations []*publishedGeneration) *publicationReadState {
	state := &publicationReadState{generations: generations, assets: make(map[string]*publishedGeneration)}
	if len(generations) == 0 {
		return state
	}
	active := generations[0]
	for _, asset := range active.manifest.Assets {
		state.assets[asset.PublicPath] = active
	}
	return state
}

func (s *PublicationService) cleanupRetired(retired []*publishedGeneration) {
	for _, generation := range retired {
		if generation == nil {
			continue
		}
		generation.retired.Store(true)
		if generation.readers.Load() == 0 {
			_ = os.RemoveAll(generation.root)
		}
	}
}

func (s *PublicationService) String() string {
	if s == nil {
		return "publication-disabled"
	}
	return fmt.Sprintf("publication(%s)", s.siteID)
}
