// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

func executorName(j *journal) string {
	return "rixa-install-" + j.Draft.OperationID + "-" + strconv.Itoa(j.ExecutorGeneration) + ".service"
}

func (m *Manager) executorFenced(ctx context.Context, j *journal) bool {
	if j.ExecutorGeneration < 1 || j.ExecutorGeneration > 16 || !j.ExecutorAcknowledged && j.Attempts["executor"] != "verified" {
		return false
	}
	unit := executorName(j)
	b, _ := m.run(ctx, "/usr/bin/systemctl", "show", "--property=LoadState", "--property=ActiveState", "--property=MainPID", "--property=ControlGroup", unit)
	p := properties(b)
	if p["ActiveState"] != "inactive" && p["ActiveState"] != "failed" {
		return false
	}
	if p["MainPID"] != "0" {
		return false
	}
	if group := p["ControlGroup"]; group != "" {
		// Kernel populated=0 covers descendants too, not only the former main PID.
		if group != "/system.slice/"+unit {
			return false
		}
		events, e := os.ReadFile(filepath.Join("/sys/fs/cgroup", group, "cgroup.events"))
		if e != nil || !strings.Contains("\n"+string(events), "\npopulated 0\n") {
			return false
		}
	}
	return p["LoadState"] == "loaded" || p["LoadState"] == "not-found"
}
func (m *Manager) resumable(ctx context.Context, j *journal) bool {
	if j.Frozen == "" || j.ExecutorGeneration >= 16 || j.State != "applying" && j.State != "reconciliation-required" || !m.executorFenced(ctx, j) {
		return false
	}
	if j.Stage == "postflight" || j.Stage == "remove" {
		return false
	} // unknown authentication/destructive acknowledgements are not replayed
	h, e := hashFile(filepath.Join(managerRoot, j.ManagerSHA256))
	if e != nil || h != j.ManagerSHA256 || !trustedExecutable(filepath.Join(managerRoot, j.ManagerSHA256)) {
		return false
	}
	observed, e := databaseIdentities(ctx, j.Draft)
	if e != nil || !reflect.DeepEqual(observed, j.DatabaseTargets) {
		return false
	}
	return true
}

// Resume starts a new fixed executor only after the previous unit and every
// descendant are observed quiesced. Frozen code/targets and product-owned
// effect-specific reconciliation govern continuation; no generic retry exists.
func (m *Manager) Resume(ctx context.Context, id string) (Result, error) {
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return Result{}, e
	}
	defer unlock()
	j, e := m.load(id)
	if e != nil {
		return Result{}, e
	}
	if !m.resumable(ctx, j) {
		return result(j), ErrState
	}
	exe, e := os.Executable()
	if e != nil {
		return result(j), ErrAuthority
	}
	h, e := hashFile(exe)
	if e != nil || h != j.ManagerSHA256 {
		return result(j), ErrAuthority
	}
	return m.queue(ctx, j)
}
func (m *Manager) queue(ctx context.Context, j *journal) (Result, error) {
	j.ExecutorGeneration++
	j.ExecutorAcknowledged = false
	j.State, j.Code, j.Message = "applying", "queued", "A fixed installation executor is queued independently of this terminal; refresh status."
	j.Attempts["executor"] = "attempted"
	if e := m.save(j); e != nil {
		return Result{}, e
	}
	manager := filepath.Join(managerRoot, j.ManagerSHA256)
	_, e := m.run(ctx, "/usr/bin/systemd-run", "--unit="+strings.TrimSuffix(executorName(j), ".service"), "--property=Type=exec", "--property=RuntimeMaxSec=600", "--property=KillMode=control-group", "--collect", manager, "manage", "internal-apply", j.Draft.OperationID)
	if e != nil {
		j.State, j.Code, j.Message = "reconciliation-required", "executor-acknowledgement-unknown", "Inspect the exact executor "+executorName(j)+" and retained state; its unknown start is not replayed."
		_ = m.save(j)
		return result(j), ErrState
	}
	j.ExecutorAcknowledged = true
	if e = m.save(j); e != nil {
		return Result{}, e
	}
	return result(j), nil
}
func writeExpected(path string, b []byte, mode os.FileMode, uid int) error {
	if e := writeNew(path, b, mode); e == nil {
		return nil
	}
	old, e := readPrivate(path, uid, int64(len(b))+1)
	if mode != 0600 {
		if e == nil {
			return ErrState
		}
		i, se := os.Lstat(path)
		if se != nil || !i.Mode().IsRegular() || i.Mode().Perm() != mode || fileUID(i) != uint32(uid) {
			return ErrState
		}
		old, e = os.ReadFile(path)
	}
	if e != nil || !reflect.DeepEqual(old, b) {
		return ErrState
	}
	return nil
}
