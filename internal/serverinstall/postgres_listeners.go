// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type postgresSocketOwner struct{ pid, fd int }
type postgresStockSocket struct {
	protocol, endpoint, inode, unit, executable, account string
	owners                                               []postgresSocketOwner
}

var postgresSocketProcess = regexp.MustCompile(`pid=([0-9]+),fd=([0-9]+)`)
var postgresSocketInode = regexp.MustCompile(`\bino:([0-9]+)\b`)

// Inspect UDP too: a dedicated-host affirmation must not hide a shared service
// behind the TCP-only check. Stock DHCP/DNS/LLMNR are positively identified.
func parsePostgresStockSockets(data []byte) ([]postgresStockSocket, error) {
	var sockets []postgresStockSocket
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 7 || len(sockets) >= 128 {
			return nil, ErrPrerequisite
		}
		s := postgresStockSocket{protocol: f[0], endpoint: f[4]}
		if s.protocol == "tcp" && f[1] != "LISTEN" || s.protocol == "udp" && f[1] != "UNCONN" {
			return nil, ErrPrerequisite
		}
		host, port, e := net.SplitHostPort(s.endpoint)
		if e != nil {
			return nil, ErrPrerequisite
		}
		host = strings.SplitN(host, "%", 2)[0]
		ip := net.ParseIP(host)
		if ip == nil {
			return nil, ErrPrerequisite
		}
		switch {
		case s.protocol == "tcp" && port == "22":
			s.unit, s.executable, s.account = "ssh.service", "/usr/sbin/sshd", "root"
		case (s.protocol == "tcp" || s.protocol == "udp") && port == "53" && ip.IsLoopback():
			s.unit, s.executable, s.account = "systemd-resolved.service", "/usr/lib/systemd/systemd-resolved", "systemd-resolve"
		case (s.protocol == "tcp" || s.protocol == "udp") && port == "5355" && ip.IsUnspecified():
			s.unit, s.executable, s.account = "systemd-resolved.service", "/usr/lib/systemd/systemd-resolved", "systemd-resolve"
		case s.protocol == "udp" && port == "68" && ip.To4() != nil && !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsMulticast():
			s.unit, s.executable, s.account = "systemd-networkd.service", "/usr/lib/systemd/systemd-networkd", "systemd-network"
		default:
			return nil, ErrPrerequisite
		}
		inodes := postgresSocketInode.FindAllStringSubmatch(line, -1)
		if len(inodes) != 1 || inodes[0][1] == "0" {
			return nil, ErrPrerequisite
		}
		s.inode = inodes[0][1]
		matches := postgresSocketProcess.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 || len(matches) > 8 {
			return nil, ErrPrerequisite
		}
		seen := map[postgresSocketOwner]bool{}
		for _, match := range matches {
			pid, pe := strconv.Atoi(match[1])
			fd, fe := strconv.Atoi(match[2])
			owner := postgresSocketOwner{pid, fd}
			if pe != nil || fe != nil || pid <= 1 || fd < 0 || seen[owner] {
				return nil, ErrPrerequisite
			}
			seen[owner] = true
			s.owners = append(s.owners, owner)
		}
		sockets = append(sockets, s)
	}
	return sockets, nil
}

func (b *postgresBackend) stockListeners(ctx context.Context, data []byte) ([]string, error) {
	sockets, e := parsePostgresStockSockets(data)
	if e != nil {
		return nil, e
	}
	var identities []string
	for _, s := range sockets {
		identity, e := b.stockSocketIdentity(ctx, s)
		if e != nil {
			return nil, e
		}
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	return identities, nil
}

func (b *postgresBackend) stockSocketIdentity(ctx context.Context, s postgresStockSocket) (string, error) {
	show := func() (map[string]string, error) {
		out, e := b.m.run(ctx, "/usr/bin/systemctl", "show", "--property=ActiveState", "--property=MainPID", "--property=FragmentPath", "--property=DropInPaths", "--property=ControlGroup", "--property=User", s.unit)
		return properties(out), e
	}
	p, e := show()
	if e != nil || p["ActiveState"] != "active" || p["DropInPaths"] != "" || p["ControlGroup"] != "/system.slice/"+s.unit {
		return "", ErrPrerequisite
	}
	if s.account == "root" && p["User"] != "" && p["User"] != "root" || s.account != "root" && p["User"] != s.account {
		return "", ErrAuthority
	}
	main, e := strconv.Atoi(p["MainPID"])
	if e != nil || main <= 1 {
		return "", ErrAuthority
	}
	fragment, e := filepath.EvalSymlinks(p["FragmentPath"])
	if e != nil || fragment != "/usr/lib/systemd/system/"+s.unit || !trustedExecutable(fragment) || !trustedExecutable(s.executable) {
		return "", ErrAuthority
	}
	unitHash, e := hashFile(fragment)
	if e != nil {
		return "", e
	}
	exeHash, e := hashFile(s.executable)
	if e != nil {
		return "", e
	}
	account, e := user.Lookup(s.account)
	if e != nil {
		return "", ErrAuthority
	}
	uid, ue := strconv.ParseUint(account.Uid, 10, 32)
	gid, ge := strconv.ParseUint(account.Gid, 10, 32)
	if ue != nil || ge != nil {
		return "", ErrAuthority
	}
	for _, owner := range s.owners {
		if owner.pid != main {
			return "", ErrAuthority
		}
		proc := "/proc/" + strconv.Itoa(owner.pid)
		image, e := os.Readlink(proc + "/exe")
		if e != nil || image != s.executable || postgresCgroup(owner.pid) != p["ControlGroup"] {
			return "", ErrAuthority
		}
		status, e := os.ReadFile(proc + "/status")
		if e != nil || !postgresStockCredentials(status, uint32(uid), uint32(gid)) {
			return "", ErrAuthority
		}
		target, e := os.Readlink(proc + "/fd/" + strconv.Itoa(owner.fd))
		if e != nil || target != "socket:["+s.inode+"]" {
			return "", ErrAuthority
		}
	}
	again, e := show()
	if e != nil || again["MainPID"] != p["MainPID"] || again["ActiveState"] != "active" || again["FragmentPath"] != p["FragmentPath"] || again["DropInPaths"] != "" || again["ControlGroup"] != p["ControlGroup"] || again["User"] != p["User"] {
		return "", ErrState
	}
	// Exclude ephemeral PID/FD/inode/queues while binding the actual service,
	// executable/unit bytes and native account. Reboots do not stale a review.
	return fmt.Sprintf("%s %s %s %s %s %d:%d", s.protocol, s.endpoint, s.unit, exeHash, unitHash, uid, gid), nil
}

func postgresStockCredentials(status []byte, uid, gid uint32) bool {
	seen := map[string]bool{}
	for _, line := range strings.Split(string(status), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || key != "Uid" && key != "Gid" && key != "Groups" {
			continue
		}
		if seen[key] {
			return false
		}
		seen[key] = true
		fields := strings.Fields(value)
		if key != "Groups" && len(fields) != 4 {
			return false
		}
		expected := gid
		if key == "Uid" {
			expected = uid
		}
		for _, f := range fields {
			v, e := strconv.ParseUint(f, 10, 32)
			if e != nil || uint32(v) != expected {
				return false
			}
		}
	}
	return seen["Uid"] && seen["Gid"] && seen["Groups"]
}
