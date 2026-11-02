// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

func (m *Manager) inAddSiteExecutor(j *addSiteJournal) bool {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) == 3 && fields[2] == "/system.slice/"+addSiteExecutorName(j) {
			return true
		}
	}
	return false
}

// RunAddSiteOperation is a fixed systemd executor, not a root shell. The
// shared lifecycle lock serializes every stage and its durable receipt.
func (m *Manager) RunAddSiteOperation(ctx context.Context, id string) (Result, error) {
	wait, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var unlock func()
	var err error
	for {
		unlock, err = m.lock(wait, false)
		if !errors.Is(err, ErrBusy) {
			break
		}
		select {
		case <-wait.Done():
			return Result{}, wait.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	j, err := m.loadAddSite(id)
	if err != nil {
		return Result{}, err
	}
	if j.State != "applying" || j.Frozen == "" || !m.inAddSiteExecutor(j) {
		return addSiteResult(j), ErrAuthority
	}
	image, err := currentManagerImage()
	if err != nil || digest(image) != j.ManagerSHA256 || !m.addSiteManagerIntact(j) {
		return m.addSiteFailure(j, "manager-drift", "The frozen Add Site executor identity changed.", ErrManagerChanged)
	}
	claim, err := m.addSiteMarker("pending-add-site")
	if err != nil || claim != id {
		return m.addSiteFailure(j, "topology-conflict", "The pending topology owner differs.", ErrState)
	}
	pending, err := m.updateMarker("pending-update")
	if err != nil || pending != "" {
		return m.addSiteFailure(j, "update-conflict", "A conflicting update owner exists.", ErrState)
	}
	parent, current, err := m.addSitePredecessors(j)
	if err != nil {
		return m.addSiteFailure(j, "predecessor-changed", "The original or active updated installation lineage changed.", err)
	}
	j.Attempts["executor"] = "verified"
	if err = m.saveAddSite(j); err != nil {
		return Result{}, err
	}
	for _, stage := range addSiteStages {
		if j.Attempts[stage] == "verified" {
			continue
		}
		if err = ctx.Err(); err != nil {
			return m.addSiteFailure(j, "interrupted", "The Add Site executor stopped; retained effects need inspection.", err)
		}
		j.Stage = stage
		attempted := j.Attempts[stage] == "attempted"
		if !attempted {
			j.Attempts[stage] = "attempted"
			j.Code, j.Message = "stage-attempted", "Executing "+stage+"."
			if err = m.saveAddSite(j); err != nil {
				return Result{}, err
			}
		}
		var principal string
		principal, err = m.executeAddSiteStage(ctx, j, parent, current, stage, attempted)
		if err != nil {
			return m.addSiteFailure(j, "stage-outcome-unknown", "The "+stage+" stage needs exact reconciliation; inspect the retained executor and diagnostics.", err)
		}
		if stage == "bootstrap-site-b" {
			if !principalSyntax.MatchString(principal) {
				return m.addSiteFailure(j, "principal-identity-unknown", "The site-B account receipt cannot be bound.", ErrState)
			}
			j.SiteBPrincipal = principal
		}
		j.Attempts[stage] = "verified"
		j.Code, j.Message = "stage-verified", stage+" verified."
		if err = m.saveAddSite(j); err != nil {
			return Result{}, err
		}
	}
	j.State, j.Code, j.Message = "installed", "installed", "Site-B migration, isolated administrator, preserved topology, TLS and runtime have verified."
	j.Draft.SiteBPassword = ""
	if err = m.saveAddSite(j); err != nil {
		return Result{}, err
	}
	if err = m.clearAddSiteClaim(id); err != nil {
		return addSiteResult(j), err
	}
	return addSiteResult(j), nil
}

func (m *Manager) executeAddSiteStage(ctx context.Context, j *addSiteJournal, parent, current *journal, stage string, attempted bool) (string, error) {
	if j == nil || parent == nil || current == nil {
		return "", ErrState
	}
	observed, err := databaseIdentities(ctx, j.Draft.withInstallation(parent.Draft))
	if err != nil || !reflect.DeepEqual(observed, j.DatabaseTargets) {
		return "", ErrState
	}
	original, err := m.addSiteOriginal(j)
	if err != nil {
		return "", err
	}
	config, env, candidate, err := m.addSiteConfigState(j, parent)
	if err != nil {
		return "", err
	}
	baseEnv := environmentFile(parent.Draft)
	targetEnv := environmentFile(j.Draft.withInstallation(parent.Draft))
	before := bytes.Equal(config, original) && bytes.Equal(env, baseEnv)
	after := bytes.Equal(config, candidate) && bytes.Equal(env, targetEnv)
	switch stage {
	case "prepare", "migrate", "bootstrap-site-b", "stop":
		if !before || m.checkOwnedService(current) != nil {
			return "", ErrState
		}
	case "activate":
		if !attempted && !before {
			return "", ErrState
		}
	default:
		if !after {
			return "", ErrState
		}
	}
	// All database and stage work before stop must leave the live one-site
	// listener unchanged. Once stop is verified, no product check runs live.
	if stage == "prepare" || stage == "migrate" || stage == "bootstrap-site-b" || stage == "stop" && !attempted {
		if m.loadedUpdateService(ctx, current, true) != nil || m.updateUnitFileState(ctx) != "enabled" {
			return "", ErrState
		}
	}
	if stage == "activate" || stage == "check" ||
		stage == "start" && !attempted {
		if !m.updateMigrationFenced(ctx) {
			return "", ErrState
		}
	}
	switch stage {
	case "prepare", "migrate", "bootstrap-site-b", "check":
		stageConfig := candidate
		if stage == "bootstrap-site-b" { // Principal is only known after durable bootstrap.
			stageConfig, err = composeAddSiteBeforePrincipal(original, j, parent)
			if err != nil {
				return "", err
			}
		}
		receipt, err := m.addSiteChildStage(ctx, j, parent, stage, stageConfig)
		if err != nil {
			return "", err
		}
		return receipt.Principal, nil
	case "stop":
		if attempted {
			if m.updateMigrationFenced(ctx) {
				return "", nil
			}
			return "", ErrState // do not repeat an uncertain service stop
		}
		if err = updateEditorialPreflight(ctx, current); err != nil {
			return "", err
		}
		if _, err = m.run(ctx, "/usr/bin/systemctl", "disable", "--now", "rixa.service"); err != nil {
			return "", err
		}
		if !m.updateMigrationFenced(ctx) {
			return "", ErrState
		}
		return "", nil
	case "activate":
		if err = m.activateAddSiteBytes(parent, original, candidate, baseEnv, targetEnv); err != nil {
			return "", err
		}
		return "", nil
	case "start":
		view := addSiteView(current, j)
		if m.checkOwnedService(view) != nil || m.loadedUpdateService(ctx, view, false) != nil {
			return "", ErrState
		}
		if attempted {
			if m.updateUnitFileState(ctx) != "enabled" || m.loadedUpdateService(ctx, view, true) != nil {
				return "", ErrState // never blindly restart an unknown start
			}
			return "", nil
		}
		if _, err = m.run(ctx, "/usr/bin/systemctl", "enable", "--now", "rixa.service"); err != nil {
			return "", err
		}
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			if m.updateUnitFileState(ctx) == "enabled" && m.loadedUpdateService(ctx, view, true) == nil {
				return "", nil
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
		}
		return "", ErrPrerequisite
	case "postflight":
		if attempted {
			return "", ErrState
		} // login/logout acknowledgement is not replayed
		view := addSiteView(current, j)
		if m.updateUnitFileState(ctx) != "enabled" || m.checkOwnedService(view) != nil ||
			m.loadedUpdateService(ctx, view, true) != nil {
			return "", ErrState
		}
		if err = m.updateHTTPPostflight(ctx, view); err != nil {
			return "", err
		}
		if !principalSyntax.MatchString(j.SiteBPrincipal) || j.Draft.SiteBPassword == "" {
			return "", ErrState
		}
		client, err := m.localClient(parent)
		if err != nil {
			return "", err
		}
		defer client.CloseIdleConnections()
		return "", verifyLogin(ctx, client, j.Draft.SiteBOrigin, j.Draft.SiteBLogin,
			j.Draft.SiteBPassword, j.SiteBPrincipal, "/admin/content/")
	case "commit":
		if attempted {
			marker, err := m.addSiteMarker("active-add-site")
			if err != nil || marker != j.Draft.OperationID {
				return "", ErrState
			}
			return "", nil
		}
		view := addSiteView(current, j)
		if m.updateUnitFileState(ctx) != "enabled" || m.checkOwnedService(view) != nil ||
			m.loadedUpdateService(ctx, view, true) != nil ||
			m.updateHTTPPostflight(ctx, view) != nil {
			return "", ErrState
		}
		marker, err := m.addSiteMarker("active-add-site")
		if err != nil || marker != "" {
			return "", ErrState
		}
		return "", writeNew(filepath.Join(m.stateRoot, "active-add-site"), []byte(j.Draft.OperationID+"\n"), 0600)
	default:
		return "", ErrInput
	}
}

func composeAddSiteBeforePrincipal(original []byte, j *addSiteJournal, parent *journal) ([]byte, error) {
	plan, err := composeAddSiteConfig(original, environmentFile(parent.Draft), parent, j.Draft.AddSiteInspectionDraft, "")
	if err != nil {
		return nil, err
	}
	return plan.Configuration, nil
}

// Each file is changed atomically with the correct owner and mode. If the
// executor dies between the two renames, a fenced Resume accepts only the
// exact old/new bytes and finishes the missing rename. It never overwrites an
// unrelated edit, follows a symlink or treats a partial pair as success.
func (m *Manager) activateAddSiteBytes(parent *journal, original, candidate, oldEnv, newEnv []byte) error {
	if parent == nil || !trustedRootParent(unitPath) ||
		privateDir(runtimeRoot, int(parent.UID), false) != nil {
		return ErrState
	}
	if err := replaceOwnedExact("/etc/rixa/service.env", oldEnv, newEnv, 0, 0); err != nil {
		return err
	}
	return replaceOwnedExact(runtimeConfigPath, original, candidate, parent.UID, parent.GID)
}
func replaceOwnedExact(path string, old, next []byte, uid, gid uint32) error {
	current, err := readPrivate(path, int(uid), 1<<20)
	if err != nil || (!bytes.Equal(current, old) && !bytes.Equal(current, next)) {
		return ErrState
	}
	if bytes.Equal(current, next) {
		return nil
	}
	if !safePath(path) || (path != runtimeConfigPath && !trustedRootParent(path)) ||
		(path == runtimeConfigPath && privateDir(runtimeRoot, int(uid), false) != nil) {
		return ErrState
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".rixa-topology-")
	if err != nil {
		return ErrState
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err = f.Chmod(0600); err == nil {
		err = f.Chown(int(uid), int(gid))
	}
	if err == nil {
		_, err = f.Write(next)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return ErrState
	}
	if err = os.Rename(temp, path); err != nil {
		return ErrState
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return ErrState
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return ErrState
	}
	observed, err := readPrivate(path, int(uid), 1<<20)
	if err != nil || !bytes.Equal(observed, next) {
		return ErrState
	}
	return nil
}
