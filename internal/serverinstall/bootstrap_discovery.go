// SPDX-License-Identifier: MPL-2.0

package serverinstall

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func bootstrapCurrentImage() (string, string, error) {
	image, e := currentManagerImage()
	if e != nil {
		return "", "", e
	}
	path, e := os.Executable()
	if e != nil {
		return "", "", ErrState
	}
	path, e = filepath.EvalSymlinks(path)
	return path, digest(image), e
}
func (b *bootstrapBackend) cleanHost(ctx context.Context) error {
	osRelease, e := os.ReadFile("/etc/os-release")
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" || e != nil || !strings.Contains("\n"+string(osRelease), "\nID=debian\n") || !strings.Contains("\n"+string(osRelease), "\nVERSION_ID=\"13\"\n") {
		return ErrPrerequisite
	}
	if _, e := user.Lookup("rixa"); e == nil {
		return ErrState
	} else {
		var unknown user.UnknownUserError
		if !errors.As(e, &unknown) {
			return ErrState
		}
	}
	if _, e := user.LookupGroup("rixa"); e == nil {
		return ErrState
	} else {
		var unknown user.UnknownGroupError
		if !errors.As(e, &unknown) {
			return ErrState
		}
	}
	out, _ := b.m.run(ctx, "/usr/bin/systemctl", "show", "--property=LoadState", "--value", "rixa.service")
	if strings.TrimSpace(string(out)) != "not-found" {
		return ErrState
	}
	for _, path := range b.footprint {
		if _, e := os.Lstat(path); !errors.Is(e, os.ErrNotExist) {
			return ErrState
		}
	}
	return nil
}
func (b *bootstrapBackend) executable(path, hash, root string) error {
	if !hashSyntax.MatchString(hash) || !safePath(path) || filepath.Dir(path) != root && !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return ErrState
	}
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		i, e := os.Lstat(p)
		if e != nil || !i.IsDir() || i.Mode().Perm()&0022 != 0 || fileUID(i) != uint32(b.m.ownerUID) {
			return ErrState
		}
		if p == root {
			break
		}
		if p == "/" {
			return ErrState
		}
	}
	if b.m.ownerUID == 0 && !trustedRootParent(path) {
		return ErrState
	}
	i, e := os.Lstat(path)
	if e != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0022 != 0 || i.Mode().Perm()&0111 == 0 || fileUID(i) != uint32(b.m.ownerUID) {
		return ErrState
	}
	actual, e := hashFile(path)
	if e != nil || actual != hash {
		return ErrState
	}
	return nil
}
func (b *bootstrapBackend) frozen(manager, helper string) (string, string, error) {
	path := filepath.Join(b.managerRoot, manager)
	hp := filepath.Join(b.managerRoot, "helpers", helper, "gh")
	if b.executable(path, manager, b.managerRoot) != nil || b.executable(hp, helper, b.managerRoot) != nil {
		return "", "", ErrState
	}
	return path, hp, nil
}
func (b *bootstrapBackend) marker(name string) (string, error) {
	data, e := readPrivate(filepath.Join(b.m.stateRoot, name), b.m.ownerUID, 128)
	if errors.Is(e, os.ErrNotExist) {
		return "", nil
	}
	id := strings.TrimSuffix(string(data), "\n")
	if e != nil || string(data) != id+"\n" || !operationSyntax.MatchString(id) {
		return "", ErrState
	}
	return id, nil
}
func (b *bootstrapBackend) discover(ctx context.Context) (r BootstrapDiscovery, err error) {
	r = BootstrapDiscovery{Schema: bootstrapSchema, State: "blocked", Code: "inspection-required", Message: "Retained native state or resource ownership requires inspection; no installation was started.", Actions: []string{"status"}}
	if err = b.m.authorize(); err != nil {
		return r, err
	}
	if err = ctx.Err(); err != nil {
		return r, err
	}
	if b.postgres != nil {
		r.Postgres, err = b.postgres(ctx)
		if err != nil || len(r.Postgres) > 128 {
			return r, ErrState
		}
	}
	if _, e := os.Lstat(b.m.stateRoot); errors.Is(e, os.ErrNotExist) {
		if err = b.clean(ctx); err != nil {
			if errors.Is(err, ErrPrerequisite) {
				r.Code = "unsupported-host"
				r.Message = "New native setup requires the documented Debian 13 Linux amd64/systemd profile and supplied TLS. Existing resources were retained."
			}
			return r, err
		}
		r.State, r.Code, r.Message, r.Actions = "clean", "not-installed", "No native installation or conflicting owned resource was found.", []string{"install", "prerequisites", "status"}
		return r, nil
	}
	if err = privateDir(b.m.stateRoot, b.m.ownerUID, false); err != nil {
		return r, err
	}
	postgresIDs := map[string]bool{}
	for _, pg := range r.Postgres {
		if !operationSyntax.MatchString(pg.OperationID) || postgresIDs[pg.OperationID] {
			return r, ErrState
		}
		postgresIDs[pg.OperationID] = true
	}
	entries, e := os.ReadDir(b.m.stateRoot)
	if e != nil || len(entries) > 512 {
		return r, ErrState
	}
	owner, e := b.marker("installation")
	if e != nil {
		return r, e
	}
	pending, e := b.marker("pending-update")
	if e != nil {
		return r, e
	}
	active, e := b.marker("active-update")
	if e != nil {
		return r, e
	}
	if owner == "" && (pending != "" || active != "") {
		return r, ErrState
	}
	installs := map[string]*journal{}
	updates := map[string]*updateJournal{}
	for _, entry := range entries {
		name := entry.Name()
		if name == ".lock" {
			i, se := os.Lstat(filepath.Join(b.m.stateRoot, name))
			if se != nil || !i.Mode().IsRegular() || i.Mode().Perm() != 0600 || fileUID(i) != uint32(b.m.ownerUID) {
				return r, ErrState
			}
			continue
		}
		if name == "bootstrap" {
			if privateDir(filepath.Join(b.m.stateRoot, name), b.m.ownerUID, false) != nil {
				return r, ErrState
			}
			continue
		}
		if name == "postgres" {
			if b.postgres == nil {
				return r, ErrState
			}
			continue
		}
		if strings.HasSuffix(name, ".update.json") {
			id := strings.TrimSuffix(name, ".update.json")
			u, e := b.m.loadUpdate(id)
			if e != nil {
				return r, ErrState
			}
			updates[id] = u
		} else if strings.HasSuffix(name, ".json") {
			id := strings.TrimSuffix(name, ".json")
			j, e := b.m.load(id)
			if e != nil {
				return r, ErrState
			}
			installs[id] = j
		}
	}
	for id := range installs {
		if postgresIDs[id] {
			return r, ErrState
		}
	}
	for id := range updates {
		if postgresIDs[id] || installs[id] != nil {
			return r, ErrState
		}
	}
	if owner != "" && installs[owner] == nil {
		return r, ErrState
	}
	var original []byte
	if owner != "" {
		j := installs[owner]
		if j.Frozen == "" {
			return r, ErrState
		}
		original, e = readPrivate(b.m.statePath(owner), b.m.ownerUID, maxStateBytes)
		if e != nil {
			return r, e
		}
	}
	for id, j := range installs {
		if j.Frozen != "" && id != owner {
			return r, ErrState
		}
		op := BootstrapOperation{OperationKind: "install", OperationID: id, State: j.State, Release: j.Draft.Release}
		if j.Frozen != "" {
			op.ManagerPath, op.HelperPath, e = b.frozen(j.ManagerSHA256, j.HelperSHA256)
			if e != nil {
				return r, e
			}
			op.ManagerSHA256 = j.ManagerSHA256
		} else {
			op.Message = "Unapplied private draft. A new review is required before effects."
			path, hash, ce := b.current()
			if ce == nil && (j.ReviewedManagerSHA256 == "" || hash == j.ReviewedManagerSHA256) {
				op.ManagerPath, op.ManagerSHA256 = path, hash
			}
		}
		r.Operations = append(r.Operations, op)
	}
	for id, u := range updates {
		if owner == "" || u.Draft.InstallationOperation != owner {
			return r, ErrState
		}
		op := BootstrapOperation{OperationKind: "update", OperationID: id, InstallationOperation: owner, State: u.State, Release: u.Draft.Release}
		if u.Frozen != "" {
			if u.ParentSHA256 != digest(original) {
				return r, ErrState
			}
			op.ManagerPath = filepath.Join(b.managerRoot, u.ManagerSHA256)
			op.HelperPath = filepath.Join(b.managerRoot, "helpers", u.HelperSHA256, "gh")
			if id == active || id == pending {
				op.ManagerPath, op.HelperPath, e = b.frozen(u.ManagerSHA256, u.HelperSHA256)
				if e != nil {
					return r, e
				}
			} else {
				op.Message = "Historical operation; its exact frozen image is rechecked when opened."
			}
			op.ManagerSHA256 = u.ManagerSHA256
			if u.PriorUpdate != "" {
				prior := updates[u.PriorUpdate]
				if prior == nil || prior.State != "installed" {
					return r, ErrState
				}
				data, re := readPrivate(b.m.updatePath(u.PriorUpdate), b.m.ownerUID, maxStateBytes)
				if re != nil || digest(data) != u.PriorSHA256 {
					return r, ErrState
				}
			}
		} else {
			op.Message = "Unapplied update draft. Review with its selected trusted manager before effects."
			path, hash, ce := b.current()
			if ce == nil && (u.ReviewedManagerSHA256 == "" || hash == u.ReviewedManagerSHA256) {
				op.ManagerPath, op.ManagerSHA256 = path, hash
			}
		}
		r.Operations = append(r.Operations, op)
	}
	if active != "" {
		u := updates[active]
		if u == nil || (u.State != "installed" && u.State != "removed") {
			return r, ErrState
		}
	}
	if pending != "" {
		u := updates[pending]
		if u == nil || u.Frozen == "" || (pending != active && u.PriorUpdate != active) {
			return r, ErrState
		}
	}
	sort.Slice(r.Operations, func(i, j int) bool { return r.Operations[i].OperationID < r.Operations[j].OperationID })
	r.InstallationOperation = owner
	if pending != "" {
		r.SelectedOperation = pending
	} else if active != "" {
		r.SelectedOperation = active
	} else {
		r.SelectedOperation = owner
	}
	if len(r.Operations) == 0 {
		for _, entry := range entries {
			if entry.Name() != ".lock" && entry.Name() != "bootstrap" && !(entry.Name() == "postgres" && b.postgres != nil) {
				return r, ErrState
			}
		}
		if err = b.clean(ctx); err != nil {
			return r, err
		}
		r.State, r.Code, r.Message, r.Actions = "clean", "not-installed", "No native installation or conflicting owned resource was found.", []string{"install", "prerequisites", "status"}
		if len(r.Postgres) > 0 {
			r.State, r.Code, r.Message, r.Actions = "prerequisites", "retained-prerequisites", "Inspect the retained PostgreSQL operation before a new installation.", []string{"manage-prerequisites", "status"}
			complete := true
			for _, pg := range r.Postgres {
				if pg.State != "installed" {
					complete = false
				}
			}
			if complete {
				r.Actions = append(r.Actions, "install")
			}
		}
		return r, nil
	}
	r.State, r.Code, r.Message, r.Actions = "retained", "retained-operation", "Inspect the same retained operation with its exact manager. No operation was applied or resumed.", []string{"manage", "status"}
	if owner != "" && installs[owner].State == "installed" && pending == "" && (active == "" || updates[active].State == "installed") {
		r.Actions = append(r.Actions, "update")
	}
	return r, nil
}
func (b *bootstrapBackend) open(ctx context.Context, id string) (BootstrapRoute, error) {
	r, e := b.discover(ctx)
	if e != nil {
		return BootstrapRoute{}, e
	}
	if id == "" {
		id = r.SelectedOperation
	}
	for _, op := range r.Operations {
		if op.OperationID != id {
			continue
		}
		if op.ManagerPath == "" {
			return BootstrapRoute{}, ErrManagerChanged
		}
		if op.HelperPath != "" && (b.executable(op.ManagerPath, op.ManagerSHA256, b.managerRoot) != nil || b.executable(op.HelperPath, filepath.Base(filepath.Dir(op.HelperPath)), b.managerRoot) != nil) {
			return BootstrapRoute{}, ErrState
		}
		args := []string{"manage", "menu", "--operation", id}
		if op.OperationKind == "update" {
			args = []string{"manage", "update", "menu", "--operation", id}
		}
		return BootstrapRoute{ManagerPath: op.ManagerPath, ManagerSHA256: op.ManagerSHA256, OperationKind: op.OperationKind, OperationID: id, InstallationOperation: op.InstallationOperation, Release: op.Release, Arguments: args, HelperPath: op.HelperPath}, nil
	}
	for _, pg := range r.Postgres {
		if pg.OperationID != id {
			continue
		}
		path, hash := pg.ManagerPath, pg.ManagerSHA256
		if path == "" {
			if pg.State != "draft" && pg.State != "ready" && pg.State != "blocked" {
				return BootstrapRoute{}, ErrManagerChanged
			}
			path, hash, e = b.current()
			if e != nil {
				return BootstrapRoute{}, e
			}
		} else if b.executable(path, hash, b.managerRoot) != nil {
			return BootstrapRoute{}, ErrState
		}
		return BootstrapRoute{ManagerPath: path, ManagerSHA256: hash, OperationKind: "postgres", OperationID: id, Arguments: []string{"manage", "prerequisites", "menu", "--operation", id}}, nil
	}
	return BootstrapRoute{}, ErrInput
}
