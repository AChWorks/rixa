// SPDX-License-Identifier: MPL-2.0
package product

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Optional, explicitly isolated PostgreSQL 18 field acceptance. Normal CI
// exercises the original retained-upgrade proof without requiring host pg_dump.
// This runs only when both RIXA_RETAINED_UPGRADE_OBSERVATION=1 and
// RIXA_RETAINED_RESTORE_OBSERVATION=1 are supplied on a disposable DB host.
type retainedPublishedArchive struct {
	path string
	hash [32]byte
	size int64
}

type retainedPublishedBackupSet struct {
	databases  map[string]retainedPublishedArchive
	sites      []SiteConfig
	configPath string
	configHash [32]byte
	certPath   string
	keyPath    string
	certHash   [32]byte
	keyHash    [32]byte
}

// Copy only regular files and directories into a fresh private test-owned root.
// This is a bounded physical backup/restore fixture, not a production archival
// format or a general-purpose extractor. Symlinks and special files fail closed.
func copyRetainedProofFile(source, destination string, mode os.FileMode) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(dst, io.LimitReader(src, 32<<20+1))
	closeErr := dst.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n > 32<<20 {
		return errors.New("retained test file exceeds private backup bound")
	}
	return os.Chmod(destination, mode.Perm())
}

func copyRetainedProofTree(t *testing.T, source, destination string) {
	t.Helper()
	root, err := os.Lstat(source)
	if err != nil || !root.IsDir() {
		t.Fatal("private Media/Public source is not a directory")
	}
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Mkdir(target, info.Mode().Perm())
		}
		if !entry.Type().IsRegular() {
			return errors.New("non-regular private file in retained backup")
		}
		return copyRetainedProofFile(path, target, info.Mode())
	})
	if err != nil {
		t.Fatal("private Media/Public backup or restoration failed")
	}
}

func retainedPGTool(t *testing.T, ctx context.Context, name, dsn string, args ...string) []byte {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		(u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") ||
		u.Query().Get("sslmode") != "disable" {
		t.Fatal("restore acceptance requires explicit disposable loopback PostgreSQL DSNs")
	}
	database := strings.TrimPrefix(u.Path, "/")
	user := u.User.Username()
	password, found := u.User.Password()
	if !found || user == "" || database == "" || strings.Contains(database, "/") {
		t.Fatal("incomplete restore acceptance DSN")
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	cmd := exec.CommandContext(ctx, name, append([]string{"--host", u.Hostname(), "--port", port,
		"--username", user, "--dbname", database, "--no-password"}, args...)...)
	// Do not leak the test credential through process argv or diagnostics.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C", "LC_ALL=C",
		"PGPASSWORD=" + password, "PGSSLMODE=disable"}
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Preserve bounded, actionable test diagnostics while redacting only
		// the actual private connection and credential material.
		diagnostic := strings.ReplaceAll(string(output), dsn, "[dsn]")
		diagnostic = strings.ReplaceAll(diagnostic, password, "[credential]")
		if len(diagnostic) > 2048 {
			diagnostic = diagnostic[:2048] + "... [truncated]"
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("%s failed with exit code %d: %s", name, exit.ExitCode(), diagnostic)
		}
		t.Fatalf("%s could not execute: %T: %s", name, err, diagnostic)
	}
	return output
}

func retainedArchiveDigest(t *testing.T, path string) ([32]byte, int64) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal("retained archive cannot be opened")
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, io.LimitReader(f, 256<<20+1))
	if err != nil || size < 256 || size > 256<<20 {
		t.Fatal("retained PostgreSQL archive is empty, oversized or unreadable")
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, size
}

// Called only after the authentic v0.7 runtime has stopped and retained
// databases have been observed. No database is changed during backup.
func captureRetainedPublishedBackups(t *testing.T, ctx context.Context,
	dsns map[string]string, config Config, configPath string,
	beforeFiles map[string]string) *retainedPublishedBackupSet {
	t.Helper()
	if os.Getenv("RIXA_RETAINED_RESTORE_OBSERVATION") != "1" {
		return nil
	}
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Fatal("PostgreSQL 18 pg_dump is required for real backup/restore acceptance")
	}
	if _, err := exec.LookPath("pg_restore"); err != nil {
		t.Fatal("PostgreSQL 18 pg_restore is required for real backup/restore acceptance")
	}
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		version, err := exec.CommandContext(ctx, tool, "--version").Output()
		if err != nil || !bytes.Contains(version, []byte("(PostgreSQL) 18.")) {
			t.Fatalf("real backup/restore acceptance requires PostgreSQL 18 %s", tool)
		}
	}
	dir := t.TempDir() // private, automatically discarded with disposable Test run
	backup := &retainedPublishedBackupSet{
		databases: make(map[string]retainedPublishedArchive, 3),
	}
	backup.configPath = filepath.Join(dir, "published-config.json")
	if err := copyRetainedProofFile(configPath, backup.configPath, 0600); err != nil {
		t.Fatal("private published config could not be backed up")
	}
	configBytes, err := os.ReadFile(backup.configPath)
	if err != nil {
		t.Fatal("private published config backup is unreadable")
	}
	backup.configHash = sha256.Sum256(configBytes)
	backup.certPath = filepath.Join(dir, "published-tls-cert.pem")
	backup.keyPath = filepath.Join(dir, "published-tls-key.pem")
	for _, pair := range [][2]string{
		{config.TLS.CertFile, backup.certPath},
		{config.TLS.KeyFile, backup.keyPath},
	} {
		info, err := os.Lstat(pair[0])
		if err != nil || !info.Mode().IsRegular() ||
			copyRetainedProofFile(pair[0], pair[1], info.Mode()) != nil {
			t.Fatal("private published TLS material could not be backed up")
		}
	}
	certData, certErr := os.ReadFile(backup.certPath)
	keyData, keyErr := os.ReadFile(backup.keyPath)
	if certErr != nil || keyErr != nil {
		t.Fatal("private TLS backup is unreadable")
	}
	backup.certHash = sha256.Sum256(certData)
	backup.keyHash = sha256.Sum256(keyData)
	if len(config.Sites) < 2 {
		t.Fatal("published backup requires two active sites")
	}
	backup.sites = append([]SiteConfig(nil), config.Sites[:2]...)
	for i := range backup.sites {
		site := &backup.sites[i]
		for _, tree := range []struct {
			source string
			target *string
			label  string
		}{
			{site.MediaRoot, &site.MediaRoot, "media"},
			{site.PublicRoot, &site.PublicRoot, "public"},
		} {
			target := filepath.Join(dir, site.ID+"-"+tree.label)
			copyRetainedProofTree(t, tree.source, target)
			*tree.target = target
		}
	}
	if !reflect.DeepEqual(snapshotRetainedFiles(t, backup.sites), beforeFiles) {
		t.Fatal("copied physical Media/Public backup differs from published source")
	}
	for _, scope := range []string{"control", "a", "b"} {
		dsn := dsns[scope]
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatal("cannot observe quiesced published database")
		}
		var others int
		err = conn.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND pid <> pg_backend_pid()`).Scan(&others)
		_ = conn.Close(ctx)
		if err != nil || others != 0 {
			t.Fatalf("published %s database is not quiescent for backup", scope)
		}
		path := filepath.Join(dir, "published-"+scope+".dump")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal("cannot create private published-backup archive")
		}
		_ = file.Close()
		retainedPGTool(t, ctx, "pg_dump", dsn,
			"--format=custom", "--no-owner", "--no-privileges", "--file", path)
		h, size := retainedArchiveDigest(t, path)
		listing := retainedPGTool(t, ctx, "pg_restore", dsn, "--list", path)
		if !bytes.Contains(listing, []byte("SCHEMA")) || len(listing) < 128 {
			t.Fatalf("published %s archive lacks a readable PostgreSQL object catalog", scope)
		}
		backup.databases[scope] = retainedPublishedArchive{path: path, hash: h, size: size}
		t.Logf("RIXA_PUBLISHED_PG_BACKUP scope=%s bytes=%d sha256=%x", scope, size, h)
	}
	return backup
}

// Restore into three genuinely new PostgreSQL 18 physical identities; the
// current migrated databases are never modified. Check v0.7 compatibility
// before a second independent forward migration of the restored copy.
func proveRetainedPublishedRestore(t *testing.T, ctx context.Context, adminDSN string,
	backups *retainedPublishedBackupSet, originalDSNs map[string]string,
	baselineBinary string, config Config, configPath string,
	beforeRows, beforeFiles map[string]string, beforeConfig []byte) {
	t.Helper()
	if backups == nil {
		return
	}
	if len(backups.databases) != 3 || len(backups.sites) != 2 {
		t.Fatal("complete published database and private-file backup is required")
	}
	originalConfigBytes, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(originalConfigBytes, beforeConfig) {
		t.Fatal("original configuration differs from the frozen published backup")
	}
	restoredRoot := t.TempDir()
	sourceConfig, err := os.ReadFile(backups.configPath)
	if err != nil || sha256.Sum256(sourceConfig) != backups.configHash {
		t.Fatal("published configuration archive integrity failed")
	}
	var restoredRaw fileConfig
	if err := json.Unmarshal(sourceConfig, &restoredRaw); err != nil ||
		len(restoredRaw.Sites) < 2 {
		t.Fatal("published configuration backup cannot be reconstructed")
	}
	for i := range backups.sites {
		if restoredRaw.Sites[i].ID != backups.sites[i].ID {
			t.Fatal("published Media/Public backup site identity changed")
		}
		for _, tree := range []struct {
			source string
			target *string
			label  string
		}{
			{backups.sites[i].MediaRoot, &restoredRaw.Sites[i].MediaRoot, "media"},
			{backups.sites[i].PublicRoot, &restoredRaw.Sites[i].PublicRoot, "public"},
		} {
			target := filepath.Join(restoredRoot, backups.sites[i].ID+"-"+tree.label)
			copyRetainedProofTree(t, tree.source, target)
			*tree.target = target
		}
	}
	for _, cert := range []struct {
		source string
		target *string
		hash   [32]byte
		name   string
	}{
		{backups.certPath, &restoredRaw.TLS.CertFile, backups.certHash, "restored-cert.pem"},
		{backups.keyPath, &restoredRaw.TLS.KeyFile, backups.keyHash, "restored-key.pem"},
	} {
		data, err := os.ReadFile(cert.source)
		if err != nil || sha256.Sum256(data) != cert.hash {
			t.Fatal("published TLS archive integrity failed")
		}
		info, err := os.Lstat(cert.source)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatal("invalid published TLS archive path")
		}
		path := filepath.Join(restoredRoot, cert.name)
		if err := copyRetainedProofFile(cert.source, path, info.Mode()); err != nil {
			t.Fatal("TLS material restoration failed")
		}
		*cert.target = path
	}
	restoredConfigPath := filepath.Join(restoredRoot, "restored-rixa.json")
	writeRetainedConfig(t, restoredConfigPath, restoredRaw)
	restoredConfigBytes, err := os.ReadFile(restoredConfigPath)
	if err != nil || !reflect.DeepEqual(snapshotRetainedFiles(t, restoredRaw.Sites[:2]), beforeFiles) {
		t.Fatal("restored private site paths or configuration are incomplete")
	}
	restoredConfig, err := LoadConfig(restoredConfigPath)
	if err != nil {
		t.Fatal("restored private configuration cannot be loaded")
	}
	migratedOriginalRows := snapshotRetainedRows(t, ctx, originalDSNs)
	originalTargets := snapshotRetainedTargets(t, ctx, originalDSNs)
	restored := createTestDatabases(t, ctx, adminDSN)
	restoredTargets := snapshotRetainedTargets(t, ctx, restored)
	for _, scope := range []string{"control", "a", "b"} {
		if restoredTargets[scope] == originalTargets[scope] {
			t.Fatalf("restore reused live %s physical database identity", scope)
		}
		backup := backups.databases[scope]
		if observed, size := retainedArchiveDigest(t, backup.path); observed != backup.hash || size != backup.size {
			t.Fatalf("%s published backup mutated since capture", scope)
		}
		retainedPGTool(t, ctx, "pg_restore", restored[scope],
			"--single-transaction", "--exit-on-error", "--no-owner", "--no-privileges", backup.path)
	}
	if after := snapshotRetainedRows(t, ctx, restored); !reflect.DeepEqual(after, beforeRows) {
		t.Fatal("actual pg_restore did not reconstruct exact published v0.7 row/ledger evidence")
	}
	assertRetainedFixturePopulated(t, ctx, restored)

	restoredEnv := []string{"PATH=" + os.Getenv("PATH"), "LANG=C"}
	secretMap := map[string]string{
		"RIXA_UPGRADE_CONTROL": restored["control"],
		"RIXA_UPGRADE_A":       restored["a"],
		"RIXA_UPGRADE_B":       restored["b"],
	}
	for _, key := range []string{"RIXA_UPGRADE_CONTROL", "RIXA_UPGRADE_A", "RIXA_UPGRADE_B"} {
		restoredEnv = append(restoredEnv, key+"="+secretMap[key])
	}
	retainedCommand(t, ctx, baselineBinary, restoredEnv, "check", "--config", restoredConfigPath)
	resolved, err := restoredConfig.ResolveRuntime(ctx, mapLookup(secretMap))
	if err != nil {
		t.Fatal("cannot resolve published backup into restored three-target runtime")
	}
	prior, err := BuildRuntime(resolved, nil)
	if err != nil {
		t.Fatal("cannot construct candidate readiness check for restored v0.7 databases")
	}
	if err := Check(ctx, prior); !errors.Is(err, ErrEditorialUnavailable) {
		t.Fatalf("restored v0.7 data did not retain the migration boundary: %T", err)
	}
	if !reflect.DeepEqual(snapshotRetainedRows(t, ctx, restored), beforeRows) {
		t.Fatal("pre-migration candidate check modified restored data")
	}
	for range 2 {
		if err := Migrate(ctx, restoredConfig, mapLookup(secretMap)); err != nil {
			t.Fatal("independent post-restore forward migration failed")
		}
	}
	resolved, err = restoredConfig.ResolveRuntime(ctx, mapLookup(secretMap))
	if err != nil {
		t.Fatal("cannot resolve restored migrated site topology")
	}
	ready, err := BuildRuntime(resolved, nil)
	if err != nil || Check(ctx, ready) != nil {
		t.Fatal("restored-and-migrated candidate topology is not ready")
	}
	assertRetainedSnapshots(t, ctx, restored, restoredConfig.Sites[:2], restoredConfigPath,
		beforeRows, beforeFiles, restoredConfigBytes)
	if !reflect.DeepEqual(snapshotRetainedFiles(t, config.Sites[:2]), beforeFiles) ||
		!bytes.Equal(beforeConfig, originalConfigBytes) {
		t.Fatal("original Media/Public configuration was changed by restoration")
	}
	if !reflect.DeepEqual(snapshotRetainedRows(t, ctx, originalDSNs), migratedOriginalRows) {
		t.Fatal("restore touched the original migrated physical databases")
	}
	t.Log("RIXA_RETAINED_RESTORE pg18=18.6 debian=13 source_v0.7=published-pinned " +
		"archives=3 custom_format=true archive_hash_verified=true transaction_restore=true " +
		"physical_targets_new=true media_public_restored=true tls_config_restored=true " +
		"old_schema_rows_and_ledgers_exact=true " +
		"v0.7_check_restored=true v0.8_pre_migration_rejection=true " +
		"v0.8_restored_migration=true original_migrated_targets_untouched=true")
}
