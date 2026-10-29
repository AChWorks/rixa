// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const setupJournalSchema = "rixa.setup.v1"

// The open directory pins the private installation parent. All management kinds
// and operation IDs use the same retained lock inode; it is never unlinked.
type setupDirectory struct{ fd int }

func openSetupDirectory(target string) (*setupDirectory, error) {
	if !filepath.IsAbs(target) || filepath.Clean(target) != target || target == "/" || strings.HasPrefix(filepath.Base(target), ".rixa-") {
		return nil, ErrSetupInput
	}
	parent := filepath.Dir(target)
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil || canonical != parent {
		return nil, ErrSetupInput
	}
	fd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrSetupInput
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&0o7777 != 0o700 {
		unix.Close(fd)
		return nil, ErrSetupInput
	}
	return &setupDirectory{fd: fd}, nil
}
func (d *setupDirectory) close() { _ = unix.Close(d.fd) }

func (d *setupDirectory) lock() (func(), error) {
	fd, err := unix.Openat(d.fd, ".rixa-management.lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, ErrSetupState
	}
	f := os.NewFile(uintptr(fd), "management lock")
	if !privateSetupFile(f) {
		f.Close()
		return nil, ErrSetupState
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrSetupBusy
		}
		return nil, ErrSetupState
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = f.Close() }, nil
}
func privateSetupFile(f *os.File) bool {
	var st unix.Stat_t
	return unix.Fstat(int(f.Fd()), &st) == nil && st.Mode&unix.S_IFMT == unix.S_IFREG && st.Mode&0o7777 == 0o600 && st.Uid == uint32(os.Geteuid()) && st.Nlink == 1
}
func (d *setupDirectory) read(name string, maximum int64) ([]byte, bool, error) {
	fd, err := unix.Openat(d.fd, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, ErrSetupState
	}
	f := os.NewFile(uintptr(fd), "private setup file")
	defer f.Close()
	if !privateSetupFile(f) {
		return nil, false, ErrSetupState
	}
	data, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, false, ErrSetupState
	}
	return data, true, nil
}
func setupJournalName(target string) string {
	return ".rixa-setup-" + setupDigest([]byte(filepath.Base(target)))[:32] + ".json"
}
func (d *setupDirectory) load(target string) (*setupJournal, error) {
	data, exists, err := d.read(setupJournalName(target), 2<<20)
	if err != nil || !exists {
		return nil, err
	}
	var j setupJournal
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&j) != nil || dec.Decode(new(any)) != io.EOF || !j.valid() {
		return nil, ErrSetupState
	}
	return &j, nil
}
func (d *setupDirectory) temporary(data []byte) (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", ErrSetupState
	}
	name := ".rixa-write-" + hex.EncodeToString(id[:])
	fd, err := unix.Openat(d.fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return "", ErrSetupState
	}
	f := os.NewFile(uintptr(fd), "private setup temporary")
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = unix.Unlinkat(d.fd, name, 0)
		return "", ErrSetupState
	}
	return name, nil
}
func (d *setupDirectory) save(target string, j *setupJournal) error {
	data, err := json.Marshal(j)
	if err != nil || len(data) > 2<<20 {
		return ErrSetupState
	}
	// Refuse an unexpected state entry, even though rename would not follow it.
	if _, _, err = d.read(setupJournalName(target), 2<<20); err != nil {
		return err
	}
	name, err := d.temporary(data)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(d.fd, name, 0)
	if unix.Renameat(d.fd, name, d.fd, setupJournalName(target)) != nil || unix.Fsync(d.fd) != nil {
		return ErrSetupState
	}
	return nil
}
func (d *setupDirectory) createConfig(target string, data []byte) error {
	name, err := d.temporary(data)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(d.fd, name, 0)
	// Linux no-replace rename publishes one final directory entry atomically.
	// There is no temporary hard-link window after a lost acknowledgement.
	if err = unix.Renameat2(d.fd, name, d.fd, filepath.Base(target), unix.RENAME_NOREPLACE); err != nil {
		return ErrSetupState
	}
	if unix.Fsync(d.fd) != nil {
		return ErrSetupState
	}
	return nil
}
