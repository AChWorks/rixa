// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var postgresToolPaths = []string{"/usr/bin/pg_createcluster", "/usr/bin/pg_conftool", "/usr/bin/pg_ctlcluster", "/usr/lib/postgresql/18/bin/postgres", "/usr/lib/postgresql/18/bin/pg_controldata", "/usr/lib/postgresql/18/bin/psql", "/usr/lib/systemd/system/postgresql@.service"}

func (b *postgresBackend) recordPostgresAccount(j *postgresJournal) error {
	account, e := user.Lookup("postgres")
	if e != nil {
		return ErrState
	}
	uid, ue := strconv.ParseUint(account.Uid, 10, 32)
	gid, ge := strconv.ParseUint(account.Gid, 10, 32)
	if ue != nil || ge != nil || uid == 0 || gid == 0 || account.HomeDir != "/var/lib/postgresql" {
		return ErrAuthority
	}
	if j.PostgresUID != 0 && (j.PostgresUID != uint32(uid) || j.PostgresGID != uint32(gid)) {
		return ErrState
	}
	ids, e := account.GroupIds()
	if e != nil {
		return ErrAuthority
	}
	allowed := map[uint32]bool{uint32(gid): true}
	if ssl, se := user.LookupGroup("ssl-cert"); se == nil {
		sg, pe := strconv.ParseUint(ssl.Gid, 10, 32)
		if pe != nil || sg == 0 {
			return ErrAuthority
		}
		allowed[uint32(sg)] = true
	}
	var groups []uint32
	seen := map[uint32]bool{}
	for _, id := range ids {
		g, ge := strconv.ParseUint(id, 10, 32)
		if ge != nil || !allowed[uint32(g)] || seen[uint32(g)] {
			return ErrAuthority
		}
		seen[uint32(g)] = true
		groups = append(groups, uint32(g))
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i] < groups[j] })
	if len(j.PostgresGroups) > 0 && !reflect.DeepEqual(j.PostgresGroups, groups) {
		return ErrState
	}
	j.PostgresUID, j.PostgresGID, j.PostgresGroups = uint32(uid), uint32(gid), groups
	return nil
}
func (b *postgresBackend) recordPostgresTools(j *postgresJournal) error {
	if e := b.recordPostgresAccount(j); e != nil {
		return e
	}
	tools := map[string]string{}
	for _, path := range postgresToolPaths {
		p := b.path(path)
		h, e := hashFile(p)
		if e != nil || !trustedExecutable(p) {
			return ErrAuthority
		}
		tools[path] = h
	}
	if len(j.PostgresTools) != 0 {
		for path, hash := range tools {
			if j.PostgresTools[path] != hash {
				return ErrState
			}
		}
	}
	j.PostgresTools = tools
	return nil
}
func (b *postgresBackend) toolsIntact(j *postgresJournal) bool {
	return b.observePostgresTools(j) == nil
}
func (b *postgresBackend) observePostgresTools(j *postgresJournal) error {
	if len(j.PostgresTools) != len(postgresToolPaths) {
		return postgresObservationError(ErrState, "complete protected PostgreSQL tool bindings", nil)
	}
	for _, path := range postgresToolPaths {
		p := b.path(path)
		h, e := hashFile(p)
		if e != nil || h != j.PostgresTools[path] || !trustedExecutable(p) {
			return postgresObservationError(ErrState, "protected tool binding "+p, e)
		}
	}
	return nil
}
func (b *postgresBackend) createCluster(ctx context.Context, j *postgresJournal, attempted bool) error {
	if !b.toolsIntact(j) || b.recordPostgresAccount(j) != nil {
		return ErrState
	}
	if e := b.claimClusterNamespace(j); e != nil {
		return e
	}

	// The root-owned namespace is retained before initdb. A partial initdb is
	// never deleted/reinitialized; a complete one must pass every observation.
	if _, e := os.Lstat(b.data(j)); e == nil {
		return b.observeCluster(ctx, j)
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if _, e := os.Lstat(b.configDir(j)); !errors.Is(e, os.ErrNotExist) {
		return ErrState
	}
	cluster, _, _ := postgresNames(j)
	args := []string{"--start-conf=manual", "--port", strconv.Itoa(j.Draft.Port), "--datadir", b.data(j), "--locale=C.UTF-8", "--encoding=UTF8", "--pgoption", "listen_addresses=127.0.0.1", "--pgoption", "password_encryption=scram-sha-256", "--pgoption", "ssl=off", "18", cluster, "--", "--auth-local=peer", "--auth-host=scram-sha-256"}
	if _, e := b.long(ctx, "/usr/bin/pg_createcluster", args, nil); e != nil {
		return e
	}
	return b.observeCluster(ctx, j)
}
func (b *postgresBackend) observeCluster(ctx context.Context, j *postgresJournal) error {
	if e := b.observePostgresTools(j); e != nil {
		return e
	}
	if e := b.recordPostgresAccount(j); e != nil {
		return postgresObservationError(ErrState, "recorded PostgreSQL account identity/groups", e)
	}
	marker := filepath.Join(b.namespace(j), "owner")
	owner, e := readPrivate(marker, b.m.ownerUID, 512)
	if e != nil || string(owner) != j.Draft.OperationID+"\n"+j.Token+"\n" {
		return postgresObservationError(ErrAuthority, "private namespace owner binding "+marker, e)
	}
	for _, path := range []string{b.namespace(j), b.data(j), b.configDir(j)} {
		real, e := filepath.EvalSymlinks(path)
		if e != nil || real != path {
			return postgresObservationError(ErrAuthority, "canonical path "+path, e)
		}
	}
	info, e := os.Lstat(b.data(j))
	if e != nil {
		return postgresObservationError(ErrAuthority, "data directory "+b.data(j), e)
	}
	if !info.IsDir() || fileUID(info) != j.PostgresUID || fileGID(info) != j.PostgresGID || info.Mode().Perm() != 0700 {
		return postgresObservationError(ErrAuthority, fmt.Sprintf("data directory %s requires directory/UID=%d/GID=%d/mode=0700", b.data(j), j.PostgresUID, j.PostgresGID), nil)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return postgresObservationError(ErrState, "data device/inode unavailable at "+b.data(j), nil)
	}
	identity := fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
	if j.DataIdentity != "" && j.DataIdentity != identity {
		return postgresObservationError(ErrState, "retained data device/inode binding "+b.data(j), nil)
	}
	versionPath := filepath.Join(b.data(j), "PG_VERSION")
	version, e := readRegular(versionPath, 16)
	if e != nil || string(version) != "18\n" {
		return postgresObservationError(ErrState, "PostgreSQL 18 version file "+versionPath, e)
	}
	config, hba, autoConfig, e := b.observeConfiguration(ctx, j, func(_ context.Context, cmd *exec.Cmd) error { return cmd.Run() })
	if e != nil {
		return e
	}
	out, e := b.m.run(ctx, "/usr/lib/postgresql/18/bin/pg_controldata", "--pgdata="+b.data(j))
	if e != nil {
		return postgresObservationError(e, "physical system identifier from "+b.data(j), nil)
	}
	system := ""
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Database system identifier:") {
			system = strings.TrimSpace(strings.TrimPrefix(line, "Database system identifier:"))
		}
	}
	if !postgresNumber.MatchString(system) || j.ClusterID != "" && system != j.ClusterID {
		return postgresObservationError(ErrState, "retained PostgreSQL system identifier binding "+b.data(j), nil)
	}
	binary := j.PostgresTools["/usr/lib/postgresql/18/bin/postgres"]
	if j.BinarySHA256 != "" && j.BinarySHA256 != binary {
		return postgresObservationError(ErrState, "retained PostgreSQL binary SHA256 binding", nil)
	}
	j.ClusterID, j.DataIdentity, j.ConfigSHA256, j.AutoConfigSHA256, j.HBASHA256, j.BinarySHA256 = system, identity, digest(config), autoConfig, digest(hba), binary
	return nil
}
func postgresHBA(data []byte) bool {
	local, host := false, false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) == 4 && f[0] == "local" && (f[1] == "all" || f[1] == "replication") && (f[2] == "all" || f[2] == "postgres") && f[3] == "peer" {
			if f[1] == "all" {
				local = true
			}
			continue
		}
		if len(f) == 5 && f[0] == "host" && (f[1] == "all" || f[1] == "replication") && f[2] == "all" && (f[3] == "127.0.0.1/32" || f[3] == "::1/128") && f[4] == "scram-sha-256" {
			if f[1] == "all" && f[3] == "127.0.0.1/32" {
				host = true
			}
			continue
		}
		return false
	}
	return local && host
}
func (b *postgresBackend) start(ctx context.Context, j *postgresJournal) error {
	if e := b.observeCluster(ctx, j); e != nil {
		return e
	}
	startPath := filepath.Join(b.configDir(j), "start.conf")
	data, e := readRegular(startPath, 4096)
	if e != nil {
		return e
	}
	state := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line != "" {
			if state != "" {
				return ErrState
			}
			state = line
		}
	}
	if state != "manual" && state != "auto" {
		return ErrState
	}
	if state == "manual" {
		if e = writeAtomic(startPath, []byte("auto\n"), 0644); e != nil {
			return e
		}
		if e = os.Chown(startPath, int(j.PostgresUID), int(j.PostgresGID)); e != nil {
			return e
		}
	}
	cluster, _, _ := postgresNames(j)
	unit := "postgresql@18-" + cluster + ".service"
	if _, e = b.m.run(ctx, "/usr/bin/systemctl", "daemon-reload"); e != nil {
		return e
	}
	if b.loaded(ctx, j) == nil && b.physicalSQLProof(ctx, j) == nil {
		return nil
	}
	if _, e = b.m.run(ctx, "/usr/bin/systemctl", "enable", "--now", unit); e != nil {
		return e
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if b.loaded(ctx, j) == nil && b.physicalSQLProof(ctx, j) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return ErrPrerequisite
}
func (b *postgresBackend) loaded(ctx context.Context, j *postgresJournal) error {
	cluster, _, _ := postgresNames(j)
	unit := "postgresql@18-" + cluster + ".service"
	out, e := b.m.run(ctx, "/usr/bin/systemctl", "show", "--property=FragmentPath", "--property=DropInPaths", "--property=ActiveState", "--property=MainPID", "--property=ExecStart", "--property=UnitFileState", unit)
	p := properties(out)
	expected := "/usr/bin/pg_ctlcluster --skip-systemctl-redirect 18-" + cluster + " start ;"
	if p["UnitFileState"] != "enabled" || strings.Count(p["ExecStart"], "argv[]=") != 1 || !strings.Contains(p["ExecStart"], "path=/usr/bin/pg_ctlcluster ;") || !strings.Contains(p["ExecStart"], "argv[]="+expected) {
		return ErrAuthority
	}
	if e != nil || p["DropInPaths"] != "" || p["ActiveState"] != "active" || (p["FragmentPath"] != "/usr/lib/systemd/system/postgresql@.service" && p["FragmentPath"] != "/lib/systemd/system/postgresql@.service") {
		return ErrState
	}
	fragment, fe := filepath.EvalSymlinks(p["FragmentPath"])
	if fe != nil || fragment != "/usr/lib/systemd/system/postgresql@.service" || !trustedExecutable(fragment) {
		return ErrAuthority
	}
	fh, fe := hashFile(fragment)
	if fe != nil || fh != j.PostgresTools[fragment] {
		return ErrAuthority
	}
	pid, e := strconv.Atoi(p["MainPID"])
	if e != nil || pid <= 1 {
		return ErrState
	}
	proc := "/proc/" + strconv.Itoa(pid)
	target, e := os.Readlink(proc + "/exe")
	if e != nil || target != "/usr/lib/postgresql/18/bin/postgres" {
		return ErrState
	}
	info, e := os.Stat(proc)
	if e != nil || fileUID(info) != j.PostgresUID {
		return ErrAuthority
	}
	status, e := os.ReadFile(proc + "/status")
	if e != nil || !postgresProcessCredentials(status, j.PostgresUID, j.PostgresGID, j.PostgresGroups) {
		return ErrAuthority
	}
	args, e := os.ReadFile(proc + "/cmdline")
	if e != nil || !bytes.Equal(args, []byte("/usr/lib/postgresql/18/bin/postgres\x00-D\x00"+b.data(j)+"\x00-c\x00config_file="+b.configDir(j)+"/postgresql.conf\x00")) {
		return ErrAuthority
	}
	if postgresCgroup(pid) != "/system.slice/system-postgresql.slice/"+unit && postgresCgroup(pid) != "/system.slice/"+unit {
		return ErrAuthority
	}
	return nil
}
func (b *postgresBackend) sql(ctx context.Context, j *postgresJournal, database, query string) ([]byte, error) {
	if e := b.physicalSQLProof(ctx, j); e != nil {
		return nil, e
	}
	return b.sqlCommand(ctx, j, database, query)
}
func (b *postgresBackend) sqlCommand(ctx context.Context, j *postgresJournal, database, query string) ([]byte, error) {
	if !b.toolsIntact(j) || b.loaded(ctx, j) != nil {
		return nil, ErrAuthority
	}
	child, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, "/usr/lib/postgresql/18/bin/psql", "--no-psqlrc", "--no-password", "--quiet", "--no-align", "--tuples-only", "--set=ON_ERROR_STOP=1", "--host=/var/run/postgresql", "--port="+strconv.Itoa(j.Draft.Port), "--username=postgres", "--dbname="+database)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: j.PostgresUID, Gid: j.PostgresGID, Groups: []uint32{j.PostgresGID}}}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=/var/lib/postgresql"}
	cmd.Dir = "/var/lib/postgresql"
	cmd.Stdin = strings.NewReader("SET search_path=pg_catalog;\n" + query)
	var output, diagnostic boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = &diagnostic
	e := cmd.Run()
	if e != nil || output.exceeded || diagnostic.exceeded {
		return nil, fmt.Errorf("%w: PostgreSQL administration: %s", ErrPrerequisite, diagnostic.String())
	}
	return bytes.TrimSpace(output.Bytes()), nil
}
func postgresOwnership(j *postgresJournal) string {
	return postgresSchema + ":" + j.Draft.OperationID + ":" + digest([]byte(j.Token))
}
func postgresRoleName(j *postgresJournal, scope string) string {
	_, control, site := postgresNames(j)
	if scope == "control" {
		return control
	}
	if scope == "site" {
		return site
	}
	return ""
}
func (b *postgresBackend) roleOwned(ctx context.Context, j *postgresJournal, scope string) error {
	name := postgresRoleName(j, scope)
	if name == "" {
		return ErrInput
	}
	out, e := b.sql(ctx, j, "postgres", "SELECT json_build_object('name',rolname,'super',rolsuper,'createdb',rolcreatedb,'createrole',rolcreaterole,'replication',rolreplication,'bypass',rolbypassrls,'login',rolcanlogin,'marker',shobj_description(oid,'pg_authid'),'members',(SELECT count(*) FROM pg_auth_members WHERE member=r.oid OR roleid=r.oid)) FROM pg_roles r WHERE rolname='"+name+"';")
	if e != nil {
		return e
	}
	if len(out) == 0 {
		return os.ErrNotExist
	}
	var r struct {
		Name        string `json:"name"`
		Super       bool   `json:"super"`
		CreateDB    bool   `json:"createdb"`
		CreateRole  bool   `json:"createrole"`
		Replication bool   `json:"replication"`
		Bypass      bool   `json:"bypass"`
		Login       bool   `json:"login"`
		Marker      string `json:"marker"`
		Members     int    `json:"members"`
	}
	if json.Unmarshal(out, &r) != nil || r.Name != name || r.Super || r.CreateDB || r.CreateRole || r.Replication || r.Bypass || !r.Login || r.Marker != postgresOwnership(j) || r.Members != 0 {
		return ErrAuthority
	}
	return nil
}
func (b *postgresBackend) role(ctx context.Context, j *postgresJournal, scope string, attempted bool) error {
	e := b.roleOwned(ctx, j, scope)
	if e == nil {
		return nil
	}
	if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	query, qe := postgresRoleCreationSQL(j, scope)
	if qe != nil {
		return qe
	}
	if _, e = b.sql(ctx, j, "postgres", query); e != nil {
		return e
	}
	return b.roleOwned(ctx, j, scope)
}

type postgresDatabaseObservation struct {
	OID      string `json:"oid"`
	Owner    string `json:"owner"`
	Marker   string `json:"marker"`
	Allow    bool   `json:"allow"`
	Template bool   `json:"template"`
}

func (b *postgresBackend) databaseObservation(ctx context.Context, j *postgresJournal, scope string) (postgresDatabaseObservation, error) {
	name := postgresRoleName(j, scope)
	if name == "" {
		return postgresDatabaseObservation{}, ErrInput
	}
	out, e := b.sql(ctx, j, "postgres", "SELECT json_build_object('oid',oid::text,'owner',pg_get_userbyid(datdba),'marker',shobj_description(oid,'pg_database'),'allow',datallowconn,'template',datistemplate) FROM pg_database WHERE datname='"+name+"';")
	if e != nil {
		return postgresDatabaseObservation{}, e
	}
	if len(out) == 0 {
		return postgresDatabaseObservation{}, os.ErrNotExist
	}
	var r postgresDatabaseObservation
	if json.Unmarshal(out, &r) != nil || !postgresNumber.MatchString(r.OID) || r.Owner != name || r.Marker != postgresOwnership(j) || !r.Allow || r.Template {
		return r, ErrAuthority
	}
	key := "site-a"
	if scope == "control" {
		key = "control"
	}
	if bound := j.DatabaseTargets[key]; bound != "" && bound != j.ClusterID+":"+r.OID {
		return r, ErrState
	}
	return r, nil
}
func (b *postgresBackend) databaseOwned(ctx context.Context, j *postgresJournal, scope string) error {
	_, e := b.databaseObservation(ctx, j, scope)
	return e
}
func (b *postgresBackend) database(ctx context.Context, j *postgresJournal, scope string, attempted bool) error {
	if e := b.roleOwned(ctx, j, scope); e != nil {
		return e
	}
	observation, e := b.databaseObservation(ctx, j, scope)
	if errors.Is(e, os.ErrNotExist) {
		name := postgresRoleName(j, scope)
		// CREATE DATABASE cannot be transactional with its ownership comment. An
		// interruption leaving an unmarked database is intentionally operator-only.
		if _, e = b.sql(ctx, j, "postgres", "CREATE DATABASE \""+name+"\" OWNER \""+name+"\" TEMPLATE template0 ENCODING 'UTF8'; COMMENT ON DATABASE \""+name+"\" IS '"+postgresOwnership(j)+"';"); e != nil {
			return e
		}
		observation, e = b.databaseObservation(ctx, j, scope)
	}
	if e != nil {
		return e
	}
	if j.DatabaseTargets == nil {
		j.DatabaseTargets = map[string]string{}
	}
	key := "site-a"
	if scope == "control" {
		key = "control"
	}
	j.DatabaseTargets[key] = j.ClusterID + ":" + observation.OID
	return b.m.savePostgres(j)
}
func (b *postgresBackend) permissions(ctx context.Context, j *postgresJournal) error {
	_, control, site := postgresNames(j)
	if _, e := b.sql(ctx, j, "postgres", "REVOKE CONNECT ON DATABASE postgres, template1 FROM PUBLIC;"); e != nil {
		return e
	}
	for _, name := range []string{control, site} {
		if _, e := b.sql(ctx, j, "postgres", "REVOKE CONNECT ON DATABASE \""+name+"\" FROM PUBLIC;"); e != nil {
			return e
		}
		if _, e := b.sql(ctx, j, name, "GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO \""+name+"\";"); e != nil {
			return e
		}
	}
	return nil
}
func postgresCredentials(j *postgresJournal) PostgresCredentials {
	_, control, site := postgresNames(j)
	dsn := func(name, password string) string {
		return (&url.URL{Scheme: "postgres", User: url.UserPassword(name, password), Host: "127.0.0.1:" + strconv.Itoa(j.Draft.Port), Path: "/" + name, RawQuery: "sslmode=disable"}).String()
	}
	return PostgresCredentials{ControlDSN: dsn(control, j.ControlPassword), SiteDSN: dsn(site, j.SitePassword)}
}
func postgresConnect(parent context.Context, dsn string) (*pgx.Conn, error) {
	config, e := pgx.ParseConfig(dsn)
	if e != nil {
		return nil, ErrInput
	}
	config.ConnectTimeout = 2 * time.Second
	config.RuntimeParams = map[string]string{"application_name": "rixa-native-postgresql-proof", "search_path": "pg_catalog"}
	config.Fallbacks = nil
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	return pgx.ConnectConfig(ctx, config)
}
func (b *postgresBackend) verify(ctx context.Context, j *postgresJournal) error {
	if b.allowedInventory(ctx, j) != nil {
		return ErrState
	}
	if e := b.observeCluster(ctx, j); e != nil {
		return postgresObservationError(ErrState, "verification configuration boundary", e)
	}
	if b.loaded(ctx, j) != nil || len(j.DatabaseTargets) != 2 {
		return ErrState
	}
	for _, scope := range []string{"control", "site"} {
		if b.roleOwned(ctx, j, scope) != nil || b.databaseOwned(ctx, j, scope) != nil {
			return ErrAuthority
		}
	}
	credentials := postgresCredentials(j)
	for _, v := range []struct{ dsn, key, other string }{{credentials.ControlDSN, "control", credentials.SiteDSN}, {credentials.SiteDSN, "site-a", credentials.ControlDSN}} {
		conn, e := postgresConnect(ctx, v.dsn)
		if e != nil {
			return ErrPrerequisite
		}
		var version int
		var server, database string
		queryCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		e = conn.QueryRow(queryCtx, "SELECT current_setting('server_version_num')::int, c.system_identifier::text, d.oid::text FROM pg_catalog.pg_control_system() c JOIN pg_catalog.pg_database d ON d.datname=pg_catalog.current_database()").Scan(&version, &server, &database)
		_ = conn.Close(context.WithoutCancel(ctx))
		cancel()
		if e != nil || version/10000 != 18 || server+":"+database != j.DatabaseTargets[v.key] {
			return ErrState
		}
		if e = postgresRequirePassword(ctx, v.dsn); e != nil {
			return e
		}
		own, _ := url.Parse(v.dsn)
		other, _ := url.Parse(v.other)
		other.User = own.User
		denied, e := postgresConnect(ctx, other.String())
		if denied != nil {
			_ = denied.Close(context.WithoutCancel(ctx))
			return ErrAuthority
		}
		var pgError *pgconn.PgError
		if !errors.As(e, &pgError) || pgError.Code != "42501" {
			return ErrAuthority
		}
	}
	return nil
}

// Claim only a positively empty namespace, or this operation's exact retained
// marker. Never put our marker around existing foreign data or configuration.
func (b *postgresBackend) claimClusterNamespace(j *postgresJournal) error {
	if e := b.publicDir(b.path(postgresDataRoot)); e != nil {
		return e
	}
	cluster, _, _ := postgresNames(j)
	siblings, e := os.ReadDir(b.path(postgresDataRoot))
	if e != nil {
		return e
	}
	for _, entry := range siblings {
		if entry.Name() != cluster {
			return ErrAuthority
		}
	}
	if e = b.publicDir(b.namespace(j)); e != nil {
		return e
	}
	marker := filepath.Join(b.namespace(j), "owner")
	expected := []byte(j.Draft.OperationID + "\n" + j.Token + "\n")
	current, e := readPrivate(marker, b.m.ownerUID, 512)
	if errors.Is(e, os.ErrNotExist) {
		entries, re := os.ReadDir(b.namespace(j))
		if re != nil || len(entries) != 0 {
			return ErrAuthority
		}
		if _, re = os.Lstat(b.configDir(j)); !errors.Is(re, os.ErrNotExist) {
			return ErrAuthority
		}
		return writeNew(marker, expected, 0600)
	}
	if e != nil || !bytes.Equal(current, expected) {
		return ErrAuthority
	}
	entries, e := os.ReadDir(b.namespace(j))
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if entry.Name() != "owner" && entry.Name() != "data" {
			return ErrAuthority
		}
	}
	return nil
}

func postgresProcessCredentials(status []byte, uid, gid uint32, groups []uint32) bool {
	var actual []uint32
	var lines []string
	seen := false
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "Groups:") {
			if seen {
				return false
			}
			seen = true
			for _, field := range strings.Fields(strings.TrimPrefix(line, "Groups:")) {
				g, e := strconv.ParseUint(field, 10, 32)
				if e != nil || g == 0 {
					return false
				}
				actual = append(actual, uint32(g))
			}
			lines = append(lines, "Groups: "+strconv.FormatUint(uint64(gid), 10))
			continue
		}
		lines = append(lines, line)
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i] < actual[j] })
	return seen && reflect.DeepEqual(actual, groups) && runtimeCredentialsMatch([]byte(strings.Join(lines, "\n")), uid, gid)
}

func (b *postgresBackend) physicalSQLProof(ctx context.Context, j *postgresJournal) error {
	if e := b.observeCluster(ctx, j); e != nil {
		return e
	}
	out, e := b.sqlCommand(ctx, j, "postgres", "SELECT json_build_object('system',(pg_catalog.pg_control_system()).system_identifier::text,'data',current_setting('data_directory'),'listen',current_setting('listen_addresses'),'port',current_setting('port'),'config',current_setting('config_file'),'hba',current_setting('hba_file'),'ident',current_setting('ident_file'),'password',current_setting('password_encryption'),'ssl',current_setting('ssl'));")
	if e != nil {
		return e
	}
	var actual postgresSecurityObservation
	if json.Unmarshal(out, &actual) != nil || !b.validSecurityObservation(j, actual) {
		return ErrAuthority
	}
	return nil
}
