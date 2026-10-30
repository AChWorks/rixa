// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const postgresShutdownUnit = "unattended-upgrades.service"
const postgresShutdownScript = "/usr/share/unattended-upgrades/unattended-upgrade-shutdown"

// Debian's idle shutdown coordinator is not the package updater. Admit only
// its exact wait-for-signal process, and bind its bytes into the reviewed host.
func (b *postgresBackend) shutdownMonitor(ctx context.Context, services []byte) (string, error) {
	found := false
	for _, line := range strings.Split(string(services), "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && f[0] == postgresShutdownUnit {
			found = true
		}
	}
	if !found {
		return "", nil
	}
	show := func() (map[string]string, error) {
		out, e := b.m.run(ctx, "/usr/bin/systemctl", "show", "--property=ActiveState", "--property=SubState", "--property=MainPID", "--property=FragmentPath", "--property=DropInPaths", "--property=ControlGroup", "--property=User", "--property=ExecStart", postgresShutdownUnit)
		return properties(out), e
	}
	p, e := show()
	if e != nil || !postgresShutdownProperties(p) {
		return "", ErrPrerequisite
	}
	pid, e := strconv.Atoi(p["MainPID"])
	if e != nil || pid <= 1 {
		return "", ErrAuthority
	}
	fragment, e := filepath.EvalSymlinks(p["FragmentPath"])
	if e != nil || fragment != "/usr/lib/systemd/system/"+postgresShutdownUnit || !trustedExecutable(fragment) || !trustedExecutable(postgresShutdownScript) {
		return "", ErrAuthority
	}
	interpreter, e := filepath.EvalSymlinks("/usr/bin/python3")
	if e != nil || !trustedExecutable(interpreter) {
		return "", ErrAuthority
	}
	script, e := readRegular(postgresShutdownScript, 1<<20)
	if e != nil || !bytes.HasPrefix(script, []byte("#!/usr/bin/python3\n")) {
		return "", ErrAuthority
	}
	proc := "/proc/" + strconv.Itoa(pid)
	image, e := os.Readlink(proc + "/exe")
	if e != nil || image != interpreter || postgresCgroup(pid) != p["ControlGroup"] {
		return "", ErrAuthority
	}
	args, e := os.ReadFile(proc + "/cmdline")
	if e != nil || !bytes.Equal(args, []byte("/usr/bin/python3\x00"+postgresShutdownScript+"\x00--wait-for-signal\x00")) {
		return "", ErrAuthority
	}
	credentials, e := os.ReadFile(proc + "/status")
	if e != nil || !postgresStockCredentials(credentials, 0, 0) {
		return "", ErrAuthority
	}
	unitHash, e := hashFile(fragment)
	if e != nil {
		return "", e
	}
	interpreterHash, e := hashFile(interpreter)
	if e != nil {
		return "", e
	}
	again, e := show()
	if e != nil || !postgresShutdownProperties(again) || again["MainPID"] != p["MainPID"] || again["ExecStart"] != p["ExecStart"] || again["FragmentPath"] != p["FragmentPath"] {
		return "", ErrState
	}
	return digest([]byte(unitHash + "\n" + interpreter + "\n" + interpreterHash + "\n" + digest(script))), nil
}

func postgresShutdownProperties(p map[string]string) bool {
	expected := postgresShutdownScript + " --wait-for-signal ;"
	return p["ActiveState"] == "active" && p["SubState"] == "running" && p["DropInPaths"] == "" &&
		(p["User"] == "" || p["User"] == "root") && p["ControlGroup"] == "/system.slice/"+postgresShutdownUnit &&
		strings.Count(p["ExecStart"], "argv[]=") == 1 &&
		strings.Contains(p["ExecStart"], "path="+postgresShutdownScript+" ;") &&
		strings.Contains(p["ExecStart"], "argv[]="+expected)
}
