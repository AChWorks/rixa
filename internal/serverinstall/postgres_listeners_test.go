// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"strings"
	"testing"
)

func stockSocketLine(proto, state, local string) string {
	return proto + " " + state + " 0 128 " + local + " 0.0.0.0:* users:((\"stock\",pid=123,fd=7)) uid:991 ino:12345 sk:1 cgroup:/system.slice/stock.service\n"
}
func TestPostgresStockSocketProfilesIncludeActualCleanDebian(t *testing.T) {
	cases := []struct{ proto, state, local, unit string }{
		{"tcp", "LISTEN", "0.0.0.0:22", "ssh.service"},
		{"tcp", "LISTEN", "[::]:22", "ssh.service"},
		{"tcp", "LISTEN", "127.0.0.53%lo:53", "systemd-resolved.service"},
		{"udp", "UNCONN", "127.0.0.54:53", "systemd-resolved.service"},
		{"tcp", "LISTEN", "0.0.0.0:5355", "systemd-resolved.service"},
		{"tcp", "LISTEN", "[::]:5355", "systemd-resolved.service"},
		{"udp", "UNCONN", "0.0.0.0:5355", "systemd-resolved.service"},
		{"udp", "UNCONN", "[::]:5355", "systemd-resolved.service"},
		{"udp", "UNCONN", "10.0.2.15%ens3:68", "systemd-networkd.service"},
	}
	for _, tc := range cases {
		t.Run(tc.proto+tc.local, func(t *testing.T) {
			rows, e := parsePostgresStockSockets([]byte(stockSocketLine(tc.proto, tc.state, tc.local)))
			if e != nil || len(rows) != 1 || rows[0].unit != tc.unit || len(rows[0].owners) != 1 || rows[0].owners[0].pid != 123 || rows[0].inode != "12345" {
				t.Fatalf("stock socket profile rejected: %v", e)
			}
		})
	}
}
func TestPostgresStockSocketPortAloneDoesNotAdmitUnownedOrSharedListeners(t *testing.T) {
	stock := stockSocketLine("tcp", "LISTEN", "0.0.0.0:5355")
	cases := []string{
		strings.Replace(stock, " users:((\"stock\",pid=123,fd=7))", "", 1),
		strings.Replace(stock, " ino:12345", "", 1),
		strings.Replace(stock, "ino:12345", "ino:0", 1),
		strings.Replace(stock, "ino:12345", "ino:12345 ino:56789", 1),
		strings.Replace(stock, "pid=123", "pid=1", 1),
		strings.Replace(stock, "0.0.0.0:5355", "0.0.0.0:80", 1),
		strings.Replace(stock, "0.0.0.0:5355", "127.0.0.1:5432", 1),
		stockSocketLine("udp", "UNCONN", "0.0.0.0:53"),
		stockSocketLine("udp", "UNCONN", "0.0.0.0:68"),
		stockSocketLine("udp", "UNCONN", "127.0.0.1:68"),
		stockSocketLine("tcp", "LISTEN", "10.0.2.15:5355"),
		stockSocketLine("udp", "UNCONN", "10.0.2.15:9999"),
		stockSocketLine("tcp", "ESTAB", "0.0.0.0:22"),
		"malformed\n",
	}
	for i, line := range cases {
		if _, e := parsePostgresStockSockets([]byte(line)); e == nil {
			t.Fatalf("unsupported/uninspectable socket%d accepted", i)
		}
	}
}
func TestPostgresStockProcessCredentialsRejectUserAndPrivilegeDrift(t *testing.T) {
	for _, tc := range []struct {
		status   string
		uid, gid uint32
	}{
		{"Uid:\t0 0 0 0\nGid:\t0 0 0 0\nGroups:\t0\n", 0, 0},
		{"Uid:\t991 991 991 991\nGid:\t992 992 992 992\nGroups:\t992\n", 991, 992},
	} {
		if !postgresStockCredentials([]byte(tc.status), tc.uid, tc.gid) {
			t.Fatal("stock service credentials rejected")
		}
		for _, bad := range []string{
			strings.Replace(tc.status, "Groups:", "Uid:", 1),
			strings.Replace(tc.status, "Groups:", "Missing:", 1),
			tc.status + "Uid: 0 0 0 0\n",
			strings.Replace(tc.status, "Groups:\t", "Groups:\t999 ", 1),
		} {
			if postgresStockCredentials([]byte(bad), tc.uid, tc.gid) {
				t.Fatal("ambiguous/extra service credentials accepted")
			}
		}
	}
}
