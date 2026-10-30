// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"strings"
	"testing"
)

func TestPostgresAdmitsOnlyStockIdleShutdownMonitor(t *testing.T) {
	good := map[string]string{"ActiveState": "active", "SubState": "running", "DropInPaths": "", "User": "", "ControlGroup": "/system.slice/" + postgresShutdownUnit, "ExecStart": "{ path=" + postgresShutdownScript + " ; argv[]=" + postgresShutdownScript + " --wait-for-signal ; ignore_errors=no ; }"}
	if !postgresShutdownProperties(good) {
		t.Fatal("stock idle shutdown monitor rejected")
	}
	for _, tc := range []struct{ key, value string }{
		{"ActiveState", "inactive"}, {"SubState", "exited"}, {"DropInPaths", "/etc/systemd/override.conf"}, {"User", "foreign"}, {"ControlGroup", "/system.slice/foreign.service"},
		{"ExecStart", "{ path=/usr/bin/unattended-upgrade ; argv[]=/usr/bin/unattended-upgrade ; }"},
		{"ExecStart", strings.Replace(good["ExecStart"], " --wait-for-signal ;", " --wait-for-signal --debug ;", 1)},
		{"ExecStart", good["ExecStart"] + " argv[]=another-command ;"},
	} {
		p := map[string]string{}
		for k, v := range good {
			p[k] = v
		}
		p[tc.key] = tc.value
		if postgresShutdownProperties(p) {
			t.Fatalf("changed shutdown monitor%s accepted", tc.key)
		}
	}
	for _, unit := range []string{"apt-daily.service", "apt-daily-upgrade.service", "foreign.service"} {
		if postgresBaseServices([]byte(unit + " loaded active running updater\n")) {
			t.Fatal("actual or foreign updater admitted")
		}
	}
	if !postgresBaseServices([]byte(postgresShutdownUnit + " loaded active running shutdown-monitor\n")) {
		t.Fatal("stock monitor cannot reach positive identity check")
	}
}
func TestPostgresAbsentShutdownMonitorNeedsNoAdditionalProbe(t *testing.T) {
	b := newPostgresBackend(testManager(t))
	b.m.run = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("absent monitor launched probe")
		return nil, nil
	}
	if h, e := b.shutdownMonitor(context.Background(), []byte("ssh.service loaded active running SSH\n")); e != nil || h != "" {
		t.Fatal("absent monitor changed host proof")
	}
}
