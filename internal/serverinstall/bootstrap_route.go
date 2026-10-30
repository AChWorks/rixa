// SPDX-License-Identifier: MPL-2.0

package serverinstall

import (
	"context"
	"path/filepath"
	"reflect"
)

func (m *Manager) CheckBootstrapRoute(ctx context.Context, r BootstrapRoute) error {
	return nativeBootstrap(m).checkRoute(ctx, r)
}
func (b *bootstrapBackend) checkRoute(ctx context.Context, r BootstrapRoute) error {
	if e := b.m.authorize(); e != nil {
		return e
	}
	// A retained operation is derived again from protected ownership/index state.
	canonical := r
	canonical.Plain, canonical.NoColor = false, false
	if retained, e := b.open(ctx, r.OperationID); e == nil && reflect.DeepEqual(retained, canonical) {
		return nil
	}
	if !operationSyntax.MatchString(r.OperationID) || !hashSyntax.MatchString(r.ManagerSHA256) || filepath.Base(r.ManagerPath) != "rixa" || filepath.Dir(filepath.Dir(r.ManagerPath)) != b.cacheRoot {
		return ErrState
	}
	archive := filepath.Base(filepath.Dir(r.ManagerPath))
	if !hashSyntax.MatchString(archive) {
		return ErrState
	}
	var receipt bootstrapReceipt
	data, e := readPrivate(filepath.Join(filepath.Dir(r.ManagerPath), ".bootstrap.json"), b.m.ownerUID, 64<<10)
	if e != nil || decode(data, &receipt) != nil || !commitSyntax.MatchString(receipt.Metadata.Source) {
		return ErrState
	}
	receipt, e = b.staged(filepath.Dir(r.ManagerPath), archive, r.Release, receipt.Metadata.Source)
	if e != nil || receipt.BinarySHA256 != r.ManagerSHA256 {
		return ErrState
	}
	d, e := b.discover(ctx)
	if e != nil {
		return e
	}
	if len(r.Arguments) == 0 {
		return ErrInput
	}
	prefill := r.Arguments[len(r.Arguments)-1]
	if filepath.Base(prefill) != "prefill.json" || !safePath(prefill) {
		return ErrState
	}
	parent := filepath.Dir(prefill)
	privateRoot := filepath.Join(b.m.stateRoot, "bootstrap")
	if filepath.Dir(parent) != privateRoot || privateDir(parent, b.m.ownerUID, false) != nil {
		return ErrState
	}
	data, e = readPrivate(prefill, b.m.ownerUID, 128<<10)
	if e != nil {
		return e
	}
	expected := []string{"manage", "menu", "--operation", r.OperationID, "--prefill", prefill}
	archivePath, bundlePath := filepath.Join(parent, "archive.tar.gz"), filepath.Join(parent, "bundle.json")
	switch r.OperationKind {
	case "install":
		if !bootstrapAllowed(d, "install") || r.InstallationOperation != "" {
			return ErrState
		}
		var draft Draft
		if decode(data, &draft) != nil || draft.OperationID != r.OperationID || draft.Release != r.Release || draft.SourceCommit != receipt.Metadata.Source || draft.ArchivePath != archivePath || draft.BundlePath != bundlePath {
			return ErrState
		}
	case "update":
		if !bootstrapAllowed(d, "update") || r.InstallationOperation != d.InstallationOperation {
			return ErrState
		}
		var draft UpdateDraft
		if decode(data, &draft) != nil || draft.OperationID != r.OperationID || draft.InstallationOperation != r.InstallationOperation || draft.Release != r.Release || draft.SourceCommit != receipt.Metadata.Source || draft.ArchivePath != archivePath || draft.BundlePath != bundlePath {
			return ErrState
		}
		expected = []string{"manage", "update", "menu", "--operation", r.OperationID, "--installation", r.InstallationOperation, "--prefill", prefill}
	default:
		return ErrInput
	}
	if !reflect.DeepEqual(expected, r.Arguments) {
		return ErrInput
	}
	frozen, e := readRegular(archivePath, 256<<20)
	if e != nil || digest(frozen) != archive {
		return ErrState
	}
	return nil
}
