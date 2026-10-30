// SPDX-License-Identifier: MPL-2.0

package serverinstall

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AChWorks/rixa/internal/releaseartifact"
	"github.com/AChWorks/rixa/internal/releasepackage"
)

type bootstrapReceipt struct {
	Schema        string                  `json:"schema"`
	ArchiveSHA256 string                  `json:"archive_sha256"`
	BinarySHA256  string                  `json:"binary_sha256"`
	TreeSHA256    string                  `json:"tree_sha256"`
	Metadata      releasepackage.Metadata `json:"metadata"`
}

func bootstrapID(prefix string) (string, error) {
	bytes := make([]byte, 8)
	if _, e := rand.Read(bytes); e != nil {
		return "", e
	}
	return prefix + hex.EncodeToString(bytes), nil
}
func bootstrapAllowed(d BootstrapDiscovery, action string) bool {
	for _, a := range d.Actions {
		if a == action {
			return true
		}
	}
	return false
}
func (b *bootstrapBackend) prepare(ctx context.Context, request BootstrapRequest) (r BootstrapPrepared, err error) {
	r = BootstrapPrepared{Schema: bootstrapSchema, State: "blocked", Code: "bootstrap-blocked", Message: "No installation or update was applied. Inspect the selected release and retained operation."}
	if e := b.m.authorize(); e != nil {
		return r, e
	}
	if request.Channel != "" && request.Channel != "stable" && request.Channel != "development" || request.ReleaseTag != "" && (request.Channel != "" || !releaseSyntax.MatchString(request.ReleaseTag) || len(request.ReleaseTag) > 128) {
		return r, ErrInput
	}
	if request.PostgresOperation != "" && (request.Update || !operationSyntax.MatchString(request.PostgresOperation)) {
		return r, ErrInput
	}
	if request.OperationID != "" && !operationSyntax.MatchString(request.OperationID) {
		return r, ErrInput
	}
	discovery, e := b.discover(ctx)
	if e != nil {
		return r, e
	}
	action := "install"
	if request.Update {
		action = "update"
	}
	if !bootstrapAllowed(discovery, action) {
		r.Code = "retained-operation-required"
		return r, ErrState
	}
	for _, pg := range discovery.Postgres {
		if request.OperationID != "" && request.OperationID == pg.OperationID {
			return r, ErrInput
		}
	}
	if request.OperationID == discovery.InstallationOperation && request.OperationID != "" {
		return r, ErrInput
	}
	unlock, e := b.m.lock(ctx, true)
	if e != nil {
		return r, e
	}
	defer unlock()
	// Discovery is read-only and rechecked under the same native effect lock.
	discovery, e = b.discover(ctx)
	if e != nil || !bootstrapAllowed(discovery, action) {
		return r, ErrState
	}
	private := filepath.Join(b.m.stateRoot, "bootstrap")
	if e = privateDir(private, b.m.ownerUID, true); e != nil {
		return r, e
	}
	session, e := os.MkdirTemp(private, "selection-")
	if e != nil {
		return r, e
	}
	if e = os.Chmod(session, 0700); e != nil {
		return r, e
	}
	output := request.OutputDir
	if output == "" {
		output = filepath.Join(session, "download")
	}
	fetched, e := b.fetch(ctx, releaseartifact.FetchRequest{OutputDir: output, Channel: request.Channel, ReleaseTag: request.ReleaseTag})
	if e != nil {
		r.Code = "release-unavailable"
		r.Message = "The explicitly selected release could not be authenticated. No channel fallback, installation, package or service effect was performed; private verification files may remain."
		return r, e
	}
	if fetched.Schema != "rixa.artifact-fetch.v1" || fetched.Status != "verified" || fetched.Code != "ok" || fetched.Verification == nil || fetched.Verification.Status != "verified" || fetched.Verification.Code != "ok" || fetched.ReleaseTag != fetched.Verification.ReleaseTag || fetched.SourceCommit != fetched.Verification.SourceCommit || !hashSyntax.MatchString(fetched.Verification.SHA256) {
		return r, ErrState
	}
	// Freeze copies before the second shared verification and the only parser.
	archive, e := readRegular(fetched.ArchivePath, releaseartifact.MaxArchiveBytes)
	if e != nil {
		return r, e
	}
	bundle, e := readRegular(fetched.BundlePath, releaseartifact.MaxBundleBytes)
	if e != nil {
		return r, e
	}
	if digest(archive) != fetched.Verification.SHA256 {
		return r, ErrState
	}
	archivePath, bundlePath := filepath.Join(session, "archive.tar.gz"), filepath.Join(session, "bundle.json")
	if e = writeNew(archivePath, archive, 0600); e != nil {
		return r, e
	}
	if e = writeNew(bundlePath, bundle, 0600); e != nil {
		return r, e
	}
	verification, e := b.verify(ctx, releaseartifact.Request{ArchivePath: archivePath, BundlePath: bundlePath, ReleaseTag: fetched.ReleaseTag, SourceCommit: fetched.SourceCommit, ExpectedSHA256: fetched.Verification.SHA256})
	if e != nil || verification.Status != "verified" || verification.Code != "ok" || verification.SHA256 != fetched.Verification.SHA256 || verification.ReleaseTag != fetched.ReleaseTag || verification.SourceCommit != fetched.SourceCommit {
		return r, ErrState
	}
	if b.m.ownerUID == 0 && !trustedRootParent(b.cacheRoot) {
		return r, ErrState
	}
	if e = mkdirOwnedNew(b.cacheRoot, 0755, b.m.ownerUID, os.Getegid()); e != nil && !errors.Is(e, os.ErrExist) {
		return r, e
	}
	cache, e := openPayloadDirectory(b.cacheRoot, 0755)
	if e != nil {
		return r, e
	}
	cache.Close()
	selected := filepath.Join(b.cacheRoot, verification.SHA256)
	if _, e = os.Lstat(selected); errors.Is(e, os.ErrNotExist) {
		out, se := newPayloadStage(b.cacheRoot)
		if se != nil {
			return r, se
		}
		// A failed private stage is retained as bounded evidence; never erase or
		// adopt a directory whose activation outcome might be unknown.
		extracted, se := b.extract(archivePath, out, releasepackage.Expected{Release: fetched.ReleaseTag, SourceCommit: fetched.SourceCommit})
		if se != nil {
			return r, packageError(se)
		}
		if !hashSyntax.MatchString(extracted.BinarySHA256) {
			return r, ErrState
		}
		for _, guide := range []string{"MANAGE.md", "VERIFY.md"} {
			info, ge := os.Lstat(filepath.Join(out, guide))
			if ge != nil || !info.Mode().IsRegular() || info.Size() < 1 {
				r.Code = "manager-unavailable"
				return r, ErrPrerequisite
			}
		}
		tree, se := bootstrapTree(out, b.m.ownerUID)
		if se != nil {
			return r, se
		}
		record := bootstrapReceipt{Schema: bootstrapSchema, ArchiveSHA256: verification.SHA256, BinarySHA256: extracted.BinarySHA256, TreeSHA256: tree, Metadata: extracted.Metadata}
		data, _ := json.Marshal(record)
		if se = writeNew(filepath.Join(out, ".bootstrap.json"), data, 0600); se != nil {
			return r, se
		}
		if se = activatePayloadStage(out, b.cacheRoot, verification.SHA256); se != nil {
			return r, se
		}
	} else if e != nil {
		return r, e
	}
	receipt, e := b.staged(selected, verification.SHA256, fetched.ReleaseTag, fetched.SourceCommit)
	if e != nil {
		r.Code = "manager-staging-conflict"
		return r, e
	}
	id := request.OperationID
	if id == "" {
		prefix := "install-"
		if request.Update {
			prefix = "update-"
		}
		id, e = bootstrapID(prefix)
		if e != nil {
			return r, e
		}
	}
	for _, pg := range discovery.Postgres {
		if id == pg.OperationID {
			return r, ErrState
		}
	}
	if _, e = os.Lstat(b.m.statePath(id)); !errors.Is(e, os.ErrNotExist) {
		return r, ErrState
	}
	if _, e = os.Lstat(b.m.updatePath(id)); !errors.Is(e, os.ErrNotExist) {
		return r, ErrState
	}
	prefill := filepath.Join(session, "prefill.json")
	kind := "install"
	var data []byte
	args := []string{"manage", "menu", "--operation", id, "--prefill", prefill}
	if request.Update {
		kind = "update"
		draft := UpdateDraft{OperationID: id, InstallationOperation: discovery.InstallationOperation, ArchivePath: archivePath, BundlePath: bundlePath, Release: fetched.ReleaseTag, SourceCommit: fetched.SourceCommit}
		data, _ = json.Marshal(draft)
		args = []string{"manage", "update", "menu", "--operation", id, "--installation", discovery.InstallationOperation, "--prefill", prefill}
	} else {
		draft := Draft{OperationID: id, ArchivePath: archivePath, BundlePath: bundlePath, Release: fetched.ReleaseTag, SourceCommit: fetched.SourceCommit}
		if request.PostgresOperation != "" {
			if b.credentials == nil {
				return r, ErrPrerequisite
			}
			credentials, ce := b.credentials(ctx, request.PostgresOperation)
			if ce != nil {
				return r, ce
			}
			draft.ControlDSN, draft.SiteDSN = credentials.ControlDSN, credentials.SiteDSN
		}
		data, _ = json.Marshal(draft)
	}
	if e = writeNew(prefill, data, 0600); e != nil {
		return r, e
	}
	r.State, r.Code, r.Message = "prepared", "manager-prepared", "Authenticated manager staged. Opening its menu is separate from review, apply and recovery."
	r.ArchivePath, r.BundlePath, r.SourceCommit = archivePath, bundlePath, fetched.SourceCommit
	r.Route = BootstrapRoute{ManagerPath: filepath.Join(selected, "rixa"), ManagerSHA256: receipt.BinarySHA256, OperationKind: kind, OperationID: id, InstallationOperation: discovery.InstallationOperation, Release: fetched.ReleaseTag, Arguments: args}
	return r, nil
}
func (b *bootstrapBackend) staged(path, archive, release, source string) (bootstrapReceipt, error) {
	var r bootstrapReceipt
	data, e := readPrivate(filepath.Join(path, ".bootstrap.json"), b.m.ownerUID, 64<<10)
	if e != nil || decode(data, &r) != nil || r.Schema != bootstrapSchema || r.ArchiveSHA256 != archive || !hashSyntax.MatchString(r.BinarySHA256) || !hashSyntax.MatchString(r.TreeSHA256) || !releasepackage.ValidIdentity(r.Metadata, releasepackage.Expected{Release: release, SourceCommit: source}) {
		return r, ErrState
	}
	if e = b.executable(filepath.Join(path, "rixa"), r.BinarySHA256, b.cacheRoot); e != nil {
		return r, e
	}
	tree, e := bootstrapTree(path, b.m.ownerUID)
	if e != nil || tree != r.TreeSHA256 {
		return r, ErrState
	}
	return r, nil
}
func bootstrapTree(root string, uid int) (string, error) {
	var records []string
	count := 0
	var total int64
	e := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return ErrState
		}
		count++
		if count > 512 {
			return ErrState
		}
		rel, re := filepath.Rel(root, path)
		if re != nil {
			return ErrState
		}
		info, re := os.Lstat(path)
		if re != nil || fileUID(info) != uint32(uid) || info.Mode().Perm()&0022 != 0 {
			return ErrState
		}
		if info.IsDir() {
			if info.Mode().Perm() != 0755 {
				return ErrState
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return ErrState
		}
		if rel == ".bootstrap.json" {
			if info.Mode().Perm() != 0600 {
				return ErrState
			}
			return nil
		}
		if strings.ContainsAny(rel, "\r\n\x00") {
			return ErrState
		}
		total += info.Size()
		if total > 256<<20 {
			return ErrState
		}
		data, re := readRegular(path, 64<<20)
		if re != nil {
			return re
		}
		records = append(records, rel+"\x00"+info.Mode().Perm().String()+"\x00"+digest(data))
		return nil
	})
	if e != nil {
		return "", e
	}
	sort.Strings(records)
	return digest([]byte(strings.Join(records, "\n"))), nil
}
