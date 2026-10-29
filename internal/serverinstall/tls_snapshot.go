// SPDX-License-Identifier: MPL-2.0
package serverinstall

import "path/filepath"

func (m *Manager) tlsSnapshotPath(j *journal, key bool) string {
	suffix, hash := ".tls-cert.", j.CertSHA256
	if key {
		suffix, hash = ".tls-key.", j.KeySHA256
	}
	return filepath.Join(m.stateRoot, j.Draft.OperationID+suffix+hash)
}

// Read and bind both inputs before copying either. Snapshots are addressed by
// their reviewed bytes so an editable pre-freeze draft never overwrites an older
// snapshot or becomes pinned to the first certificate it happened to copy.
func (m *Manager) snapshotTLSInputs(j *journal) []Problem {
	if j.Frozen != "" {
		return []Problem{{"tls_cert", "frozen-operation", "An immutable operation cannot replace its retained TLS inputs."}}
	}
	inputs := []struct {
		source, hash, field string
		key                 bool
		data                []byte
	}{
		{source: j.Draft.TLSCertPath, hash: j.CertSHA256, field: "tls_cert"},
		{source: j.Draft.TLSKeyPath, hash: j.KeySHA256, field: "tls_key", key: true},
	}
	for i := range inputs {
		input := &inputs[i]
		b, e := readRegular(input.source, 1<<20)
		if e != nil || digest(b) != input.hash {
			return []Problem{{input.field, "evidence-changed", "The TLS input changed while preparing its protected copy. Inspect the selected material and review again."}}
		}
		input.data = b
	}
	for _, input := range inputs {
		target := m.tlsSnapshotPath(j, input.key)
		if e := writeNew(target, input.data, 0600); e != nil {
			old, e := readPrivate(target, m.ownerUID, 1<<20)
			if e != nil || digest(old) != input.hash {
				return []Problem{{input.field, "snapshot-conflict", "The protected TLS snapshot cannot be created or does not match the reviewed bytes. Inspect the private operation state and filesystem; unknown files are never overwritten."}}
			}
		}
	}
	return nil
}
