// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/AChWorks/achrix/installation"
	"golang.org/x/sys/unix"
)

func postgresInExecutor(j *postgresJournal) bool {
	return postgresCgroup(os.Getpid()) == "/system.slice/"+postgresExecutorName(j)
}
func postgresCgroup(pid int) string {
	b, e := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if e != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.SplitN(line, ":", 3)
		if len(f) == 3 && f[0] == "0" {
			return f[2]
		}
	}
	return ""
}
func (m *Manager) RunPostgresOperation(ctx context.Context, id string) (PostgresResult, error) {
	wait, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var unlock func()
	var e error
	for {
		unlock, e = m.lock(wait, false)
		if !errors.Is(e, ErrBusy) {
			break
		}
		select {
		case <-wait.Done():
			return PostgresResult{}, wait.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	if e != nil {
		return PostgresResult{}, e
	}
	defer unlock()
	j, e := m.loadPostgres(id)
	if e != nil {
		return PostgresResult{}, e
	}
	if j.State != "applying" || j.Frozen == "" || !postgresInExecutor(j) {
		return postgresResult(j), ErrAuthority
	}
	image, e := currentManagerImage()
	if e != nil || digest(image) != j.ManagerSHA256 || !postgresManagerIntact(j) {
		return m.postgresFail(j, "manager-drift", "The actual or retained prerequisite manager changed; use the exact owning operation.", e)
	}
	owner, e := m.postgresOwner()
	if e != nil || owner != id {
		return m.postgresFail(j, "prerequisite-owner", "The protected prerequisite owner does not match this operation.", e)
	}
	b := newPostgresBackend(m)
	if !b.reviewedAPT(j) || b.allowedInventory(ctx, j) != nil {
		return m.postgresFail(j, "package-evidence-changed", "Signed metadata, helper identity or installed package state changed; no unknown package effects are replayed.", nil)
	}
	if j.Attempts["common"] == "" {
		baseline, host, ce := b.cleanHost(ctx, j)
		if ce != nil || host != j.HostSHA256 || !reflect.DeepEqual(baseline, j.Baseline) {
			return m.postgresFail(j, "host-evidence-changed", "The clean dedicated host scope changed before prerequisite effects; inspect ownership.", ce)
		}
	}
	j.Attempts["executor"] = "verified"
	for _, stage := range postgresStages {
		if j.Attempts[stage] == "verified" {
			continue
		}
		if e = ctx.Err(); e != nil {
			return m.postgresFail(j, "interrupted", "Execution stopped; inspect the exact retained phase before continuation.", e)
		}
		j.Stage = stage
		attempted := j.Attempts[stage] == "attempted"
		if !attempted {
			j.Attempts[stage] = "attempted"
			j.Progress.Attempts = []installation.Attempt{{RequirementID: "owned-postgresql18", State: installation.Attempted}}
			j.Code, j.Message = "stage-attempted", "Executing prerequisite "+stage+"."
			if e = m.savePostgres(j); e != nil {
				return PostgresResult{}, e
			}
		}
		if e = b.execute(ctx, j, stage, attempted); e != nil {
			return m.postgresFail(j, "stage-outcome-unknown", "The "+stage+" phase did not verify. Inspect the exact executor and protected diagnostic; partial packages/initdb or unmarked database ownership require operator reconciliation.", e)
		}
		j.Attempts[stage] = "verified"
		j.Code = "stage-verified"
		if e = m.savePostgres(j); e != nil {
			return PostgresResult{}, e
		}
	}
	j.Progress.Attempts = []installation.Attempt{{RequirementID: "owned-postgresql18", State: installation.Verified}}
	j.State, j.Code, j.Message = "installed", "installed", "Owned PostgreSQL18 prerequisite and separate loopback/password databases verified. Credentials remain private; ordinary Rixa removal retains this prerequisite."
	if e = m.savePostgres(j); e != nil {
		return PostgresResult{}, e
	}
	return postgresResult(j), nil
}
func (b *postgresBackend) execute(ctx context.Context, j *postgresJournal, stage string, attempted bool) error {
	if !b.reviewedAPT(j) || b.allowedInventory(ctx, j) != nil {
		return ErrState
	}
	switch stage {
	case "download":
		return b.download(ctx, j)
	case "common":
		if e := b.persistRepository(j); e != nil {
			return e
		}
		if e := b.installPackages(ctx, j, "common"); e != nil {
			return e
		}
		return b.recordPostgresAccount(j)
	case "suppress-main":
		return b.suppressMain(j)
	case "server":
		if !b.mainSuppressed(j) {
			return ErrState
		}
		var e error
		if e = b.installPackages(ctx, j, "server"); e != nil {
			return e
		}
		if _, e = os.Lstat(b.path("/etc/postgresql/18/main")); !errors.Is(e, os.ErrNotExist) {
			return ErrState
		}
		return b.recordPostgresTools(j)
	case "cluster":
		return b.createCluster(ctx, j, attempted)
	case "start":
		return b.start(ctx, j)
	case "control-role":
		return b.role(ctx, j, "control", attempted)
	case "site-role":
		return b.role(ctx, j, "site", attempted)
	case "control-db":
		return b.database(ctx, j, "control", attempted)
	case "site-db":
		return b.database(ctx, j, "site", attempted)
	case "permissions":
		return b.permissions(ctx, j)
	case "verify":
		return b.verify(ctx, j)
	default:
		return ErrInput
	}
}
func (b *postgresBackend) allowedInventory(ctx context.Context, j *postgresJournal) error {
	actual, e := b.inventory(ctx)
	if e != nil {
		return e
	}
	for name, version := range j.Baseline {
		if actual[name] != version {
			return ErrState
		}
	}
	for name, version := range actual {
		if old := j.Baseline[name]; old != "" {
			if old != version {
				return ErrState
			}
			continue
		}
		if postgresPackageVersionFor(j.Packages, name) != version {
			return ErrState
		}
	}
	return nil
}
func (b *postgresBackend) phasePackages(j *postgresJournal, phase string) []PostgresPackage {
	if phase == "common" {
		return j.CommonPackages
	}
	if phase == "server" {
		return j.Packages
	}
	return nil
}
func (b *postgresBackend) download(ctx context.Context, j *postgresJournal) error {
	if len(j.Artifacts) > 0 {
		return b.verifyArtifacts(ctx, j)
	}
	args := []string{"--download-only", "--assume-yes", "--no-remove", "--no-upgrade", "--no-install-recommends", "install"}
	args = append(args, postgresPackageArguments(j.Packages)...)
	if _, e := b.apt(ctx, j, args); e != nil {
		return e
	}
	dir := filepath.Join(b.cache(j), "archives")
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) > 128 {
		return ErrState
	}
	artifacts := map[string]postgresArtifact{}
	seen := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if name == "lock" || name == "partial" {
			continue
		}
		if !strings.HasSuffix(name, ".deb") || entry.IsDir() {
			return ErrState
		}
		path := filepath.Join(dir, name)
		p, h, e := b.inspectArchive(ctx, path)
		if e != nil || postgresPackageVersionFor(j.Packages, p.Name) != p.Version || seen[p.Name] {
			return ErrState
		}
		artifacts[path] = postgresArtifact{p, h}
		seen[p.Name] = true
	}
	if len(artifacts) != len(j.Packages) {
		return ErrState
	}
	j.Artifacts = artifacts
	return b.m.savePostgres(j)
}
func (b *postgresBackend) inspectArchive(ctx context.Context, path string) (PostgresPackage, string, error) {
	if !trustedExecutable(path) {
		return PostgresPackage{}, "", ErrAuthority
	}
	h, e := hashFile(path)
	if e != nil {
		return PostgresPackage{}, "", e
	}
	out, e := b.m.run(ctx, "/usr/bin/dpkg-deb", "--show", "--showformat=${Package}\t${Version}\t${Architecture}\n", path)
	f := strings.Split(strings.TrimSuffix(string(out), "\n"), "\t")
	if e != nil || len(f) != 3 || (f[2] != "amd64" && f[2] != "all") {
		return PostgresPackage{}, "", ErrState
	}
	p := PostgresPackage{f[0], f[1]}
	if !validPostgresPackages([]PostgresPackage{p}) {
		return PostgresPackage{}, "", ErrState
	}
	return p, h, nil
}
func (b *postgresBackend) verifyArtifacts(ctx context.Context, j *postgresJournal) error {
	if len(j.Artifacts) != len(j.Packages) {
		return ErrState
	}
	for path, a := range j.Artifacts {
		if filepath.Dir(path) != filepath.Join(b.cache(j), "archives") {
			return ErrState
		}
		p, h, e := b.inspectArchive(ctx, path)
		if e != nil || p != a.Package || h != a.SHA256 {
			return ErrState
		}
	}
	return nil
}
func (b *postgresBackend) persistRepository(j *postgresJournal) error {
	key, e := readRegular(filepath.Join(b.cache(j), "pgdg.asc"), 64<<10)
	if e != nil || digest(key) != j.KeySHA256 || validatePGDGKey(key) != nil {
		return ErrState
	}
	if e = writeExpected(b.path("/usr/share/keyrings/rixa-pgdg.asc"), key, 0644, b.m.ownerUID); e != nil {
		return e
	}
	return writeExpected(b.path("/etc/apt/sources.list.d/rixa-pgdg.sources"), b.postgresSources(j, true), 0644, b.m.ownerUID)
}
func (b *postgresBackend) installPackages(ctx context.Context, j *postgresJournal, phase string) error {
	if e := b.verifyArtifacts(ctx, j); e != nil {
		return e
	}
	actual, e := b.inventory(ctx)
	if e != nil {
		return e
	}
	required := b.phasePackages(j, phase)
	if len(required) == 0 {
		return ErrInput
	}
	var missing []PostgresPackage
	for _, p := range required {
		if actual[p.Name] == "" {
			missing = append(missing, p)
		} else if actual[p.Name] != p.Version {
			return ErrState
		}
	}
	if len(missing) == 0 {
		return nil
	}
	manager := filepath.Join(managerRoot, j.ManagerSHA256)
	hook := manager + " manage internal-postgres-package-guard " + j.Draft.OperationID + " " + phase
	args := []string{"--assume-yes", "--no-download", "--no-remove", "--no-upgrade", "--no-install-recommends", "-o", "DPkg::Pre-Install-Pkgs::=" + hook, "install"}
	args = append(args, postgresPackageArguments(required)...)
	if _, e = b.apt(ctx, j, args); e != nil {
		return e
	}
	if e = b.allowedInventory(ctx, j); e != nil {
		return e
	}
	actual, e = b.inventory(ctx)
	if e != nil {
		return e
	}
	for _, p := range required {
		if actual[p.Name] != p.Version {
			return ErrState
		}
	}
	return nil
}

// RunPostgresPackageGuard is a read-only APT transaction boundary. APT owns its
// native locks; the parent owns the native lifecycle flock. This child must not
// reacquire that flock or perform recovery/mutation.
func (m *Manager) RunPostgresPackageGuard(ctx context.Context, id, phase string, input io.Reader) error {
	if e := m.authorize(); e != nil {
		return e
	}
	if phase != "common" && phase != "server" {
		return ErrInput
	}
	j, e := m.loadPostgres(id)
	if e != nil {
		return e
	}
	if j.State != "applying" || j.Stage != phase || j.Attempts[phase] != "attempted" || !postgresInExecutor(j) || !postgresManagerIntact(j) {
		return ErrAuthority
	}
	image, e := currentManagerImage()
	if e != nil || digest(image) != j.ManagerSHA256 {
		return ErrManagerChanged
	}
	if !postgresAPTLockAncestor(j) {
		return ErrAuthority
	}
	b := newPostgresBackend(m)
	if !b.reviewedAPT(j) || b.allowedInventory(ctx, j) != nil {
		return ErrState
	}
	data, e := io.ReadAll(io.LimitReader(input, (64<<10)+1))
	if e != nil || len(data) > 64<<10 {
		return ErrInput
	}
	actual, e := b.inventory(ctx)
	if e != nil {
		return e
	}
	if phase == "server" && !b.mainSuppressed(j) {
		return ErrAuthority
	}
	if phase == "common" && actual["postgresql-common"] == "" {
		if _, e := user.Lookup("postgres"); e == nil {
			return ErrAuthority
		} else {
			var unknown user.UnknownUserError
			if !errors.As(e, &unknown) {
				return ErrAuthority
			}
		}
	}
	expected := map[string]bool{}
	for _, p := range b.phasePackages(j, phase) {
		if actual[p.Name] == "" {
			expected[p.Name] = true
		} else if actual[p.Name] != p.Version {
			return ErrState
		}
	}
	return postgresGuardFiles(data, j.Artifacts, expected, filepath.Join(b.cache(j), "archives"), func(path string) (PostgresPackage, string, error) { return b.inspectArchive(ctx, path) })
}
func postgresAPTLockAncestor(j *postgresJournal) bool {
	f, e := os.OpenFile("/var/lib/dpkg/lock-frontend", os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if e != nil {
		return false
	}
	defer f.Close()
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if e = unix.FcntlFlock(f.Fd(), unix.F_GETLK, &lock); e != nil || lock.Type == unix.F_UNLCK || lock.Pid <= 0 {
		return false
	}
	pid := os.Getppid()
	for depth := 0; depth < 8 && pid > 1; depth++ {
		proc := "/proc/" + strconv.Itoa(pid)
		if pid == int(lock.Pid) {
			target, e := os.Readlink(proc + "/exe")
			if e != nil || target != "/usr/bin/apt-get" || postgresCgroup(pid) != "/system.slice/"+postgresExecutorName(j) {
				return false
			}
			info, e := os.Stat(proc)
			if e != nil || fileUID(info) != 0 {
				return false
			}
			h, e := hashFile(target)
			return e == nil && h == j.Helpers[target]
		}
		status, e := os.ReadFile(proc + "/status")
		if e != nil {
			return false
		}
		next := 0
		for _, line := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(line, "PPid:") {
				next, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PPid:")))
			}
		}
		if next <= 0 || next == pid {
			return false
		}
		pid = next
	}
	return false
}

// Default common tooling must actually read our final dedicated include. Do
// not install a server on the assumption that a suppression file is effective.
func (b *postgresBackend) commonIncludesSuppression() bool {
	path := b.path("/etc/postgresql-common/createcluster.conf")
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || fileUID(info) != uint32(b.m.ownerUID) || info.Mode().Perm()&0022 != 0 {
		return false
	}
	data, e := readRegular(path, 64<<10)
	if e != nil {
		return false
	}
	last := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line != "" {
			last = line
		}
	}
	return last == "include_dir '/etc/postgresql-common/createcluster.d'"
}
func (b *postgresBackend) suppressMain(j *postgresJournal) error {
	if !b.commonIncludesSuppression() {
		return ErrPrerequisite
	}
	if _, e := os.Lstat(b.path("/etc/postgresql/18/main")); !errors.Is(e, os.ErrNotExist) {
		return ErrState
	}
	dir := b.path("/etc/postgresql-common/createcluster.d")
	if e := b.publicDir(dir); e != nil {
		return e
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if entry.Name() != "rixa.conf" {
			return ErrAuthority
		}
	}
	if e = writeExpected(filepath.Join(dir, "rixa.conf"), []byte("create_main_cluster = false\n"), 0644, b.m.ownerUID); e != nil {
		return e
	}
	if !b.mainSuppressed(j) {
		return ErrState
	}
	return nil
}
func (b *postgresBackend) mainSuppressed(j *postgresJournal) bool {
	if !b.commonIncludesSuppression() {
		return false
	}
	dir := b.path("/etc/postgresql-common/createcluster.d")
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) != 1 || entries[0].Name() != "rixa.conf" || entries[0].IsDir() {
		return false
	}
	actual, e := readRegular(filepath.Join(dir, "rixa.conf"), 1024)
	return e == nil && string(actual) == "create_main_cluster = false\n"
}

func postgresGuardFiles(data []byte, artifacts map[string]postgresArtifact, expected map[string]bool, archives string, inspect func(string) (PostgresPackage, string, error)) error {
	if len(data) > 64<<10 {
		return ErrInput
	}
	if len(expected) == 0 && len(data) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, path := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		a, ok := artifacts[path]
		if !ok || !expected[a.Package.Name] || seen[a.Package.Name] || !safePath(path) || filepath.Dir(path) != archives {
			return ErrAuthority
		}
		p, h, e := inspect(path)
		if e != nil || p != a.Package || h != a.SHA256 {
			return ErrAuthority
		}
		seen[p.Name] = true
	}
	if !reflect.DeepEqual(expected, seen) {
		return ErrAuthority
	}
	return nil
}
