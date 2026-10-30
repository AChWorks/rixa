// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/openpgp"
)

const postgresCacheRoot = "/var/cache/rixa-postgres"
const postgresDataRoot = "/var/lib/rixa-postgres"
const pgdgKeyURL = "https://www.postgresql.org/media/keys/ACCC4CF8.asc"

// Official PGDG repository identity, published by PostgreSQL:
// https://www.postgresql.org/message-id/20121206191128.GA13478%40msgid.df7cb.de
// Current scoped Signed-By repository instructions:
// https://www.postgresql.org/download/linux/debian/
const pgdgFingerprint = "B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8"

var postgresPackageName = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]{0,127}$`)
var postgresPackageVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+:~_-]{0,159}$`)
var postgresNumber = regexp.MustCompile(`^[0-9]{1,24}$`)
var postgresInstLine = regexp.MustCompile(`^Inst ([a-z0-9][a-z0-9+.-]*(?::amd64)?) \(([^ ]+) .+\)$`)

type postgresBackend struct {
	m *Manager
	// sysroot is internal test construction only; production always uses "/".
	sysroot string
	long    func(context.Context, string, []string, []string) ([]byte, error)
	key     func(context.Context) ([]byte, error)
}

func newPostgresBackend(m *Manager) *postgresBackend {
	return &postgresBackend{m: m, sysroot: "/", long: postgresCommand, key: fetchPGDGKey}
}
func (b *postgresBackend) path(p string) string {
	if b.sysroot == "/" {
		return p
	}
	return filepath.Join(b.sysroot, strings.TrimPrefix(p, "/"))
}
func (b *postgresBackend) cache(j *postgresJournal) string {
	return filepath.Join(b.path(postgresCacheRoot), digest([]byte(j.Draft.OperationID + ":" + j.Token))[:24])
}
func (b *postgresBackend) namespace(j *postgresJournal) string {
	cluster, _, _ := postgresNames(j)
	return filepath.Join(b.path(postgresDataRoot), cluster)
}
func (b *postgresBackend) data(j *postgresJournal) string {
	return filepath.Join(b.namespace(j), "data")
}
func (b *postgresBackend) configDir(j *postgresJournal) string {
	cluster, _, _ := postgresNames(j)
	return filepath.Join(b.path("/etc/postgresql/18"), cluster)
}
func postgresCommand(parent context.Context, name string, args, env []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append([]string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "DEBIAN_FRONTEND=noninteractive"}, env...)
	var out boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = &out
	e := cmd.Run()
	if e != nil || out.exceeded {
		return out.Bytes(), fmt.Errorf("%w: %s: %s", ErrPrerequisite, filepath.Base(name), strings.TrimSpace(out.String()))
	}
	return out.Bytes(), nil
}
func fetchPGDGKey(ctx context.Context) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, pgdgKeyURL, nil)
	if e != nil {
		return nil, e
	}
	resp, e := client.Do(req)
	if e != nil {
		return nil, ErrPrerequisite
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrPrerequisite
	}
	key, e := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if e != nil || len(key) > 64<<10 {
		return nil, ErrInput
	}
	return key, nil
}
func validatePGDGKey(key []byte) error {
	if len(key) == 0 || len(key) > 64<<10 {
		return ErrInput
	}
	entities, e := openpgp.ReadArmoredKeyRing(bytes.NewReader(key))
	if e != nil || len(entities) != 1 || entities[0].PrimaryKey == nil || strings.ToUpper(hex.EncodeToString(entities[0].PrimaryKey.Fingerprint[:])) != pgdgFingerprint {
		return ErrAuthority
	}
	return nil
}
func validPostgresPackages(p []PostgresPackage) bool {
	seen := map[string]bool{}
	for _, v := range p {
		if !postgresPackageName.MatchString(v.Name) || !postgresPackageVersion.MatchString(v.Version) || seen[v.Name] {
			return false
		}
		seen[v.Name] = true
	}
	return true
}
func postgresPackageVersionFor(p []PostgresPackage, name string) string {
	for _, v := range p {
		if v.Name == name {
			return v.Version
		}
	}
	return ""
}
func postgresPackageSubset(p, all []PostgresPackage) bool {
	for _, v := range p {
		if postgresPackageVersionFor(all, v.Name) != v.Version {
			return false
		}
	}
	return true
}
func postgresPackageArguments(p []PostgresPackage) []string {
	var args []string
	for _, v := range p {
		args = append(args, v.Name+"="+v.Version)
	}
	return args
}
func parsePostgresInventory(text []byte) (map[string]string, error) {
	result := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(text), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || fields[2] != "installed" {
			return nil, ErrState
		}
		name := strings.TrimSuffix(fields[0], ":amd64")
		if !postgresPackageName.MatchString(name) || !postgresPackageVersion.MatchString(fields[1]) || result[name] != "" {
			return nil, ErrState
		}
		result[name] = fields[1]
	}
	if len(result) == 0 || len(result) > 2048 {
		return nil, ErrState
	}
	return result, nil
}
func parsePostgresAPTPlan(text []byte, baseline map[string]string) ([]PostgresPackage, error) {
	var result []PostgresPackage
	seen := map[string]bool{}
	for _, line := range strings.Split(string(text), "\n") {
		if strings.HasPrefix(line, "Remv ") || strings.HasPrefix(line, "Purg ") || strings.Contains(line, "WARNING: The following packages cannot be authenticated") {
			return nil, ErrAuthority
		}
		if !strings.HasPrefix(line, "Inst ") {
			continue
		}
		f := postgresInstLine.FindStringSubmatch(line)
		if len(f) != 3 {
			return nil, ErrState
		}
		name := strings.TrimSuffix(f[1], ":amd64")
		if baseline[name] != "" || seen[name] || !postgresPackageVersion.MatchString(f[2]) || name == "postgresql" || name == "postgresql-client" || strings.HasPrefix(name, "postgresql-") && (name != "postgresql-18" && name != "postgresql-client-18" && name != "postgresql-common" && name != "postgresql-client-common") {
			return nil, ErrAuthority
		}
		result = append(result, PostgresPackage{name, f[2]})
		seen[name] = true
	}
	if len(result) > 96 || !validPostgresPackages(result) {
		return nil, ErrState
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
func (b *postgresBackend) inventory(ctx context.Context) (map[string]string, error) {
	out, e := b.m.run(ctx, "/usr/bin/dpkg-query", "--show", "--showformat=${binary:Package}\t${Version}\t${db:Status-Status}\n")
	if e != nil {
		return nil, e
	}
	return parsePostgresInventory(out)
}
func (b *postgresBackend) helpers() map[string]string {
	out := map[string]string{}
	for _, p := range []string{"/usr/bin/apt-get", "/usr/bin/dpkg-query", "/usr/bin/dpkg-deb", "/usr/share/keyrings/debian-archive-keyring.gpg"} {
		path := b.path(p)
		h, e := hashFile(path)
		if e != nil || !trustedExecutable(path) {
			return nil
		}
		out[p] = h
	}
	return out
}
func (b *postgresBackend) verifyHelpers(j *postgresJournal) bool {
	return len(j.Helpers) == 4 && reflect.DeepEqual(b.helpers(), j.Helpers)
}
func (b *postgresBackend) cleanHost(ctx context.Context, j *postgresJournal) (map[string]string, string, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return nil, "", ErrPrerequisite
	}
	osRelease, e := os.ReadFile(b.path("/etc/os-release"))
	if e != nil || !strings.Contains("\n"+string(osRelease), "\nID=debian\n") || !strings.Contains("\n"+string(osRelease), "\nVERSION_ID=\"13\"\n") || !strings.Contains("\n"+string(osRelease), "\nVERSION_CODENAME=trixie\n") {
		return nil, "", ErrPrerequisite
	}
	if _, e = os.Stat(b.path("/run/systemd/system")); e != nil {
		return nil, "", ErrPrerequisite
	}
	for _, p := range []string{"/etc/postgresql", "/etc/postgresql-common", "/var/lib/postgresql", postgresDataRoot, "/etc/apt/sources.list.d/rixa-pgdg.sources", "/usr/share/keyrings/rixa-pgdg.asc", "/usr/local/cpanel", "/usr/local/psa", "/usr/local/hestia", "/usr/local/vesta", "/www/server/panel", "/etc/rixa", unitPath, "/var/lib/rixa"} {
		if _, e = os.Lstat(b.path(p)); !errors.Is(e, os.ErrNotExist) {
			return nil, "", fmt.Errorf("%w: existing or unknown owned path %s", ErrPrerequisite, p)
		}
	}
	if _, e = user.Lookup("postgres"); e == nil {
		return nil, "", ErrPrerequisite
	} else {
		var unknown user.UnknownUserError
		if !errors.As(e, &unknown) {
			return nil, "", ErrPrerequisite
		}
	}
	baseline, e := b.inventory(ctx)
	if e != nil {
		return nil, "", e
	}
	for name := range baseline {
		if strings.HasPrefix(name, "postgresql") || strings.HasPrefix(name, "pgdg") || strings.HasPrefix(name, "pgbouncer") || strings.HasPrefix(name, "pgpool") {
			return nil, "", ErrPrerequisite
		}
	}
	services, e := b.m.run(ctx, "/usr/bin/systemctl", "list-units", "--type=service", "--state=running", "--no-legend", "--plain")
	if postgresInExecutor(j) {
		var lines []string
		for _, line := range strings.Split(string(services), "\n") {
			f := strings.Fields(line)
			if len(f) > 0 && f[0] == postgresExecutorName(j) {
				continue
			}
			lines = append(lines, line)
		}
		services = []byte(strings.Join(lines, "\n"))
	}
	if e != nil || !postgresBaseServices(services) {
		return nil, "", ErrPrerequisite
	}
	listeners, e := b.m.run(ctx, "/usr/bin/ss", "--no-header", "--listening", "--tcp", "--numeric")
	if e != nil || !postgresBaseListeners(listeners) {
		return nil, "", ErrPrerequisite
	}
	listener, e := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(j.Draft.Port))
	if e != nil {
		return nil, "", ErrPrerequisite
	}
	_ = listener.Close()
	machine, e := readRegular(b.path("/etc/machine-id"), 128)
	if e != nil || len(strings.TrimSpace(string(machine))) != 32 {
		return nil, "", ErrState
	}
	var normalizedServices []string
	for _, line := range strings.Split(string(services), "\n") {
		f := strings.Fields(line)
		if len(f) >= 4 {
			normalizedServices = append(normalizedServices, strings.Join(f[:4], " "))
		}
	}
	sort.Strings(normalizedServices)
	var normalizedListeners []string
	for _, line := range strings.Split(string(listeners), "\n") {
		f := strings.Fields(line)
		if len(f) >= 5 {
			normalizedListeners = append(normalizedListeners, f[0]+" "+f[3]+" "+f[4])
		}
	}
	sort.Strings(normalizedListeners)
	state, _ := json.Marshal(struct {
		Machine             string
		Packages            map[string]string
		Services, Listeners string
	}{strings.TrimSpace(string(machine)), baseline, strings.Join(normalizedServices, "\n"), strings.Join(normalizedListeners, "\n")})
	return baseline, digest(state), nil
}
func postgresBaseServices(out []byte) bool {
	allowed := map[string]bool{"ssh.service": true, "sshd.service": true, "dbus.service": true, "systemd-journald.service": true, "systemd-udevd.service": true, "systemd-logind.service": true, "systemd-timesyncd.service": true, "systemd-resolved.service": true, "networking.service": true, "systemd-networkd.service": true, "cron.service": true, "rsyslog.service": true, "qemu-guest-agent.service": true, "getty@tty1.service": true, "serial-getty@ttyS0.service": true, "polkit.service": true}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 4 || !allowed[f[0]] {
			return false
		}
	}
	return true
}
func postgresBaseListeners(out []byte) bool {
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 5 {
			return false
		}
		_, port, e := net.SplitHostPort(f[3])
		if e != nil {
			return false
		}
		if port != "22" && !(port == "53" && (strings.HasPrefix(f[3], "127.") || strings.HasPrefix(f[3], "[::1]"))) {
			return false
		}
	}
	return true
}
func (b *postgresBackend) publicDir(path string) error {
	if b.m.ownerUID == 0 && !trustedRootParent(path) {
		return ErrState
	}
	if e := mkdirOwnedNew(path, 0755, b.m.ownerUID, os.Getegid()); e != nil && !errors.Is(e, os.ErrExist) {
		return e
	}
	i, e := os.Lstat(path)
	if e != nil || !i.IsDir() || i.Mode().Perm() != 0755 || fileUID(i) != uint32(b.m.ownerUID) {
		return ErrState
	}
	real, e := filepath.EvalSymlinks(path)
	if e != nil || real != path {
		return ErrState
	}
	return nil
}
func (b *postgresBackend) prepareAPT(ctx context.Context, j *postgresJournal) error {
	for _, p := range []string{b.path(postgresCacheRoot), b.cache(j), filepath.Join(b.cache(j), "lists"), filepath.Join(b.cache(j), "archives")} {
		if e := b.publicDir(p); e != nil {
			return e
		}
	}
	key, e := b.key(ctx)
	if e != nil {
		return e
	}
	if e = validatePGDGKey(key); e != nil {
		return e
	}
	keyPath := filepath.Join(b.cache(j), "pgdg.asc")
	if e = writeExpected(keyPath, key, 0644, b.m.ownerUID); e != nil {
		return e
	}
	j.KeySHA256 = digest(key)
	sources := b.postgresSources(j, false)
	if e = writeExpected(filepath.Join(b.cache(j), "sources.list"), sources, 0644, b.m.ownerUID); e != nil {
		return e
	}
	if e = writeExpected(filepath.Join(b.cache(j), "apt.conf"), b.aptConfiguration(j), 0644, b.m.ownerUID); e != nil {
		return e
	}
	if _, e = b.apt(ctx, j, []string{"update", "--error-on=any"}); e != nil {
		return e
	}
	j.ListsSHA256, e = postgresListsDigest(filepath.Join(b.cache(j), "lists"))
	return e
}
func (b *postgresBackend) postgresSources(j *postgresJournal, persistent bool) []byte {
	key := filepath.Join(b.cache(j), "pgdg.asc")
	if persistent {
		return []byte("Types: deb\nURIs: https://apt.postgresql.org/pub/repos/apt\nSuites: trixie-pgdg\nComponents: main\nArchitectures: amd64\nSigned-By: /usr/share/keyrings/rixa-pgdg.asc\n")
	}
	debian := b.path("/usr/share/keyrings/debian-archive-keyring.gpg")
	return []byte("deb [arch=amd64 signed-by=" + debian + "] https://deb.debian.org/debian trixie main\ndeb [arch=amd64 signed-by=" + debian + "] https://deb.debian.org/debian trixie-updates main\ndeb [arch=amd64 signed-by=" + debian + "] https://security.debian.org/debian-security trixie-security main\ndeb [arch=amd64 signed-by=" + key + "] https://apt.postgresql.org/pub/repos/apt trixie-pgdg main\n")
}
func (b *postgresBackend) aptConfiguration(j *postgresJournal) []byte {
	cache := b.cache(j)
	return []byte("Dir::Etc::main \"/dev/null\";\nDir::Etc::parts \"-\";\nDir::Etc::sourcelist \"" + cache + "/sources.list\";\nDir::Etc::sourceparts \"-\";\nDir::Etc::preferences \"/dev/null\";\nDir::Etc::preferencesparts \"-\";\nDir::State::lists \"" + cache + "/lists\";\nDir::State::status \"" + b.path("/var/lib/dpkg/status") + "\";\nDir::Cache::archives \"" + cache + "/archives\";\nAcquire::Languages \"none\";\nAcquire::AllowInsecureRepositories \"false\";\nAcquire::AllowDowngradeToInsecureRepositories \"false\";\nAcquire::AllowWeakRepositories \"false\";\nAcquire::Check-Valid-Until \"true\";\nAPT::Get::AllowUnauthenticated \"false\";\nAPT::Install-Recommends \"false\";\nAPT::Install-Suggests \"false\";\n#clear DPkg::Pre-Invoke;\n#clear DPkg::Post-Invoke;\n#clear DPkg::Pre-Install-Pkgs;\n")
}
func (b *postgresBackend) apt(ctx context.Context, j *postgresJournal, args []string) ([]byte, error) {
	return b.long(ctx, "/usr/bin/apt-get", args, []string{"APT_CONFIG=" + filepath.Join(b.cache(j), "apt.conf")})
}
func (b *postgresBackend) simulate(ctx context.Context, j *postgresJournal, requested []PostgresPackage) ([]PostgresPackage, error) {
	args := []string{"--simulate", "--no-install-recommends", "--no-remove", "install"}
	args = append(args, postgresPackageArguments(requested)...)
	out, e := b.apt(ctx, j, args)
	if e != nil {
		return nil, e
	}
	return parsePostgresAPTPlan(out, j.Baseline)
}
func postgresListsDigest(dir string) (string, error) {
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) > 96 {
		return "", ErrState
	}
	contents := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if name == "lock" || name == "partial" || name == "auxfiles" {
			continue
		}
		if entry.IsDir() {
			return "", ErrState
		}
		h, e := hashFile(filepath.Join(dir, name))
		if e != nil {
			return "", e
		}
		contents[name] = h
	}
	if len(contents) == 0 {
		return "", ErrState
	}
	b, _ := json.Marshal(contents)
	return digest(b), nil
}
func (b *postgresBackend) reviewedAPT(j *postgresJournal) bool {
	if !b.verifyHelpers(j) {
		return false
	}
	for _, v := range []struct {
		path     string
		expected []byte
	}{{filepath.Join(b.cache(j), "apt.conf"), b.aptConfiguration(j)}, {filepath.Join(b.cache(j), "sources.list"), b.postgresSources(j, false)}} {
		actual, e := readRegular(v.path, 64<<10)
		if e != nil || !bytes.Equal(actual, v.expected) {
			return false
		}
	}
	key, e := readRegular(filepath.Join(b.cache(j), "pgdg.asc"), 64<<10)
	if e != nil || digest(key) != j.KeySHA256 || validatePGDGKey(key) != nil {
		return false
	}
	h, e := postgresListsDigest(filepath.Join(b.cache(j), "lists"))
	return e == nil && h == j.ListsSHA256
}
