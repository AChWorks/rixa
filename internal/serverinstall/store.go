// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const maxStateBytes = 64 << 10

type journal struct {
	Schema           string            `json:"schema"`
	Draft            Draft             `json:"draft"`
	State            string            `json:"state"`
	Stage            string            `json:"stage"`
	Code             string            `json:"code"`
	Message          string            `json:"message"`
	Frozen           string            `json:"frozen,omitempty"`
	CertSHA256       string            `json:"cert_sha256,omitempty"`
	KeySHA256        string            `json:"key_sha256,omitempty"`
	ArchiveSHA256    string            `json:"archive_sha256,omitempty"`
	BinarySHA256     string            `json:"binary_sha256,omitempty"`
	ManagerSHA256    string            `json:"manager_sha256,omitempty"`
	UID              uint32            `json:"uid,omitempty"`
	GID              uint32            `json:"gid,omitempty"`
	ControlPrincipal string            `json:"control_principal,omitempty"`
	SitePrincipal    string            `json:"site_principal,omitempty"`
	Attempts         map[string]string `json:"attempts"`
	UnitSHA256       string            `json:"unit_sha256,omitempty"`
	Updated          string            `json:"updated"`
}

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func draftDigest(d Draft) string {
	d.ControlPassword = ""
	d.SitePassword = ""
	b, _ := json.Marshal(d)
	return digest(b)
}
func (m *Manager) authorize() error {
	if os.Geteuid() != m.ownerUID {
		return ErrAuthority
	}
	return nil
}
func fileUID(i os.FileInfo) uint32 {
	s, ok := i.Sys().(*syscall.Stat_t)
	if !ok {
		return ^uint32(0)
	}
	return s.Uid
}
func fileGID(i os.FileInfo) uint32 {
	s, ok := i.Sys().(*syscall.Stat_t)
	if !ok {
		return ^uint32(0)
	}
	return s.Gid
}
func privateDir(path string, uid int, create bool) error {
	if !safePath(path) {
		return ErrState
	}
	if create {
		if e := os.Mkdir(path, 0700); e != nil && !errors.Is(e, os.ErrExist) {
			return ErrState
		}
	}
	real, e := filepath.EvalSymlinks(path)
	if e != nil || real != path {
		return ErrState
	}
	i, e := os.Lstat(path)
	if e != nil || !i.IsDir() || i.Mode().Perm() != 0700 || fileUID(i) != uint32(uid) {
		return ErrState
	}
	return nil
}
func readPrivate(path string, uid int, max int64) ([]byte, error) {
	f, e := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	i, e := f.Stat()
	if e != nil || !i.Mode().IsRegular() || i.Mode().Perm() != 0600 || fileUID(i) != uint32(uid) || i.Size() < 1 || i.Size() > max {
		return nil, ErrState
	}
	b, e := io.ReadAll(io.LimitReader(f, max+1))
	if e != nil || int64(len(b)) > max {
		return nil, ErrState
	}
	return b, nil
}
func decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return ErrState
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrState
	}
	return nil
}
func writeAtomic(path string, b []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, e := os.CreateTemp(dir, ".rixa-state-")
	if e != nil {
		return ErrState
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return ErrState
	}
	if e = os.Rename(name, path); e != nil {
		return ErrState
	}
	parent, e := os.Open(dir)
	if e != nil {
		return ErrState
	}
	defer parent.Close()
	if e = parent.Sync(); e != nil {
		return ErrState
	}
	return nil
}
func (m *Manager) lock(ctx context.Context, create bool) (func(), error) {
	if e := m.authorize(); e != nil {
		return nil, e
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if e := privateDir(m.stateRoot, m.ownerUID, create); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(m.stateRoot, ".lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if e != nil {
		return nil, ErrState
	}
	i, e := f.Stat()
	if e != nil || !i.Mode().IsRegular() || i.Mode().Perm() != 0600 || fileUID(i) != uint32(m.ownerUID) {
		f.Close()
		return nil, ErrState
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		f.Close()
		return nil, ErrBusy
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}
func (m *Manager) statePath(id string) string { return filepath.Join(m.stateRoot, id+".json") }
func (m *Manager) load(id string) (*journal, error) {
	if !operationSyntax.MatchString(id) {
		return nil, ErrInput
	}
	b, e := readPrivate(m.statePath(id), m.ownerUID, maxStateBytes)
	if e != nil {
		return nil, e
	}
	var j journal
	if e = decode(b, &j); e != nil || j.Schema != "rixa.native-install.v1" || j.Draft.OperationID != id || j.Attempts == nil {
		return nil, ErrState
	}
	switch j.State {
	case "draft", "ready", "applying", "installed", "blocked", "reconciliation-required", "removed":
	default:
		return nil, ErrState
	}
	if j.Frozen != "" && j.Frozen != draftDigest(j.Draft) {
		return nil, ErrState
	}
	for _, stage := range j.Attempts {
		if stage != "attempted" && stage != "verified" {
			return nil, ErrState
		}
	}
	return &j, nil
}
func (m *Manager) save(j *journal) error {
	j.Updated = time.Now().UTC().Format(time.RFC3339Nano)
	b, e := json.Marshal(j)
	if e != nil || len(b) > maxStateBytes {
		return ErrState
	}
	return writeAtomic(m.statePath(j.Draft.OperationID), b, 0600)
}
func result(j *journal) Result {
	r := Result{OperationID: j.Draft.OperationID, State: j.State, Stage: j.Stage, Code: j.Code, Message: j.Message, Release: j.Draft.Release, AdminURL: j.Draft.ControlOrigin + "/admin"}
	switch j.State {
	case "draft", "blocked":
		if j.Frozen == "" {
			r.Actions = []string{"configure", "review", "status"}
		} else {
			r.Actions = []string{"status"}
		}
	case "ready":
		r.Actions = []string{"configure", "review", "apply", "status"}
	case "installed":
		r.Actions = []string{"status", "remove"}
	default:
		r.Actions = []string{"status"}
	}
	return r
}
