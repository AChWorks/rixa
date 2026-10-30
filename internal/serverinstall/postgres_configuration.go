// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const postgresConfigurationTimeout = 10 * time.Second

func postgresObservationError(class error, predicate string, cause error) error {
	if cause != nil {
		return fmt.Errorf("%w: PostgreSQL cluster observation %s: %w", class, predicate, cause)
	}
	return fmt.Errorf("%w: PostgreSQL cluster observation %s", class, predicate)
}

// Read intended settings with PostgreSQL's own parser, including omitted
// defaults. -C prints one value and exits; it neither starts a server nor proves
// the settings of an already running server. physicalSQLProof retains that job.
// https://www.postgresql.org/docs/18/app-postgres.html
//
// run is a private command-construction test seam. The native caller always
// supplies exec.Cmd.Run; no executable, setting or execution mode is caller input.
func (b *postgresBackend) observeConfiguration(parent context.Context, j *postgresJournal, run func(context.Context, *exec.Cmd) error) ([]byte, []byte, string, error) {
	if j.PostgresUID == 0 || j.PostgresGID == 0 || len(j.PostgresGroups) < 1 || len(j.PostgresGroups) > 2 {
		return nil, nil, "", postgresObservationError(ErrAuthority, "configuration reader requires the recorded non-root PostgreSQL account/groups", nil)
	}
	primary := false
	for i, group := range j.PostgresGroups {
		if group == 0 || i > 0 && group <= j.PostgresGroups[i-1] {
			return nil, nil, "", postgresObservationError(ErrAuthority, "configuration reader has invalid recorded supplementary groups", nil)
		}
		primary = primary || group == j.PostgresGID
	}
	if !primary {
		return nil, nil, "", postgresObservationError(ErrAuthority, "configuration reader groups omit the recorded primary GID", nil)
	}

	config, hba, auto, e := b.readClusterConfiguration(j)
	if e != nil {
		return nil, nil, "", e
	}
	ctx, cancel := context.WithTimeout(parent, postgresConfigurationTimeout)
	defer cancel()
	configPath := filepath.Join(b.configDir(j), "postgresql.conf")
	// One finite list and one shared deadline cover every configuration predicate.
	// Only config_file selects a path; do not override the values being observed.
	for _, setting := range []struct{ name, want string }{
		{"data_directory", b.data(j)},
		{"port", strconv.Itoa(j.Draft.Port)},
		{"listen_addresses", "127.0.0.1"},
		{"password_encryption", "scram-sha-256"},
		{"ssl", "off"},
		{"config_file", configPath},
		{"hba_file", filepath.Join(b.configDir(j), "pg_hba.conf")},
		{"ident_file", filepath.Join(b.configDir(j), "pg_ident.conf")},
	} {
		predicate := fmt.Sprintf("setting %s from %s", setting.name, configPath)
		if e := ctx.Err(); e != nil {
			return nil, nil, "", postgresObservationError(ErrState, predicate, e)
		}
		cmd := exec.CommandContext(ctx, b.path("/usr/lib/postgresql/18/bin/postgres"), "-D", b.data(j), "-c", "config_file="+configPath, "-C", setting.name)
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: j.PostgresUID, Gid: j.PostgresGID, Groups: append([]uint32(nil), j.PostgresGroups...)}}
		cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=/var/lib/postgresql"}
		cmd.Dir = b.data(j)
		cmd.WaitDelay = time.Second
		var output, diagnostic boundedOutput
		cmd.Stdout, cmd.Stderr = &output, &diagnostic
		e := run(ctx, cmd)
		if e != nil || ctx.Err() != nil || output.exceeded || diagnostic.exceeded {
			detail := strings.TrimSpace(diagnostic.String())
			// Retain the actual native cause/context for the private failure path.
			// Only known credentials are removed; stdout is never a diagnostic.
			credentials := postgresCredentials(j)
			for _, secret := range []string{credentials.ControlDSN, credentials.SiteDSN, j.Token, j.ControlPassword, j.SitePassword, j.ControlVerifier, j.SiteVerifier} {
				if secret != "" {
					detail = strings.ReplaceAll(detail, secret, "[redacted]")
				}
			}
			if len(detail) > 8<<10 {
				detail = detail[:8<<10] + " [diagnostic truncated]"
			}
			if output.exceeded || diagnostic.exceeded {
				predicate += " exceeded the 65536-byte stdout/stderr limit"
			}
			if detail != "" {
				predicate += ": " + detail
			}
			// Preserve cancellation independently of a child exit status.
			if ce := ctx.Err(); ce != nil {
				if e != nil {
					e = fmt.Errorf("%w: %w", ce, e)
				} else {
					e = ce
				}
			}
			return nil, nil, "", postgresObservationError(ErrState, predicate, e)
		}
		value := strings.TrimSuffix(output.String(), "\n")
		if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
			return nil, nil, "", postgresObservationError(ErrState, predicate+" did not return one bounded value", nil)
		}
		if value != setting.want {
			// Unexpected configuration text can itself contain credentials. The
			// setting and required value identify this failed predicate safely.
			return nil, nil, "", postgresObservationError(ErrState, predicate+" differs from required "+strconv.Quote(setting.want), nil)
		}
	}
	// Multiple -C invocations are necessary (one setting per invocation). Keep
	// their observations bound to the same main/HBA/empty override bytes.
	afterConfig, afterHBA, afterAuto, e := b.readClusterConfiguration(j)
	if e != nil {
		return nil, nil, "", e
	}
	for _, binding := range []struct {
		path string
		same bool
	}{
		{configPath, bytes.Equal(config, afterConfig)},
		{filepath.Join(b.configDir(j), "pg_hba.conf"), bytes.Equal(hba, afterHBA)},
		{filepath.Join(b.data(j), "postgresql.auto.conf"), auto == afterAuto},
	} {
		if !binding.same {
			return nil, nil, "", postgresObservationError(ErrState, "configuration changed during queries at "+binding.path, nil)
		}
	}
	return config, hba, auto, nil
}

func (b *postgresBackend) readClusterConfiguration(j *postgresJournal) ([]byte, []byte, string, error) {
	confDirectory := filepath.Join(b.configDir(j), "conf.d")
	actualConf, e := filepath.EvalSymlinks(confDirectory)
	if e != nil || actualConf != confDirectory {
		return nil, nil, "", postgresObservationError(ErrAuthority, "canonical include directory "+confDirectory, e)
	}
	entries, e := os.ReadDir(confDirectory)
	if e != nil || len(entries) != 0 {
		return nil, nil, "", postgresObservationError(ErrAuthority, "empty include directory "+confDirectory, e)
	}
	configPath := filepath.Join(b.configDir(j), "postgresql.conf")
	config, e := readRegular(configPath, 1<<20)
	if e != nil {
		return nil, nil, "", postgresObservationError(e, "bounded regular configuration "+configPath, nil)
	}
	hbaPath := filepath.Join(b.configDir(j), "pg_hba.conf")
	hba, e := readRegular(hbaPath, 64<<10)
	if e != nil || !postgresHBA(hba) {
		return nil, nil, "", postgresObservationError(ErrAuthority, "peer/SCRAM authentication file "+hbaPath, e)
	}
	auto, e := b.observeAutoConfig(j)
	if e != nil {
		return nil, nil, "", postgresObservationError(e, "empty owned override "+filepath.Join(b.data(j), "postgresql.auto.conf"), nil)
	}
	for _, binding := range []struct{ path, retained, current string }{
		{configPath, j.ConfigSHA256, digest(config)},
		{hbaPath, j.HBASHA256, digest(hba)},
	} {
		if binding.retained != "" && binding.retained != binding.current {
			return nil, nil, "", postgresObservationError(ErrState, "retained SHA256 binding "+binding.path, nil)
		}
	}
	return config, hba, auto, nil
}
