// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// This dedicated profile has no ALTER SYSTEM settings. Bind even the empty
// generated override file; a changed or redirected HBA must never be adopted.
func (b *postgresBackend) observeAutoConfig(j *postgresJournal) (string, error) {
	path := filepath.Join(b.data(j), "postgresql.auto.conf")
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || fileUID(info) != j.PostgresUID || fileGID(info) != j.PostgresGID {
		return "", ErrAuthority
	}
	data, e := readRegular(path, 64<<10)
	if e != nil || !postgresEmptyAutoConfig(data) {
		return "", ErrAuthority
	}
	hash := digest(data)
	if j.AutoConfigSHA256 != "" && j.AutoConfigSHA256 != hash {
		return "", ErrState
	}
	return hash, nil
}

func postgresEmptyAutoConfig(data []byte) bool {
	if strings.ContainsRune(string(data), '\x00') {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return false
		}
	}
	return true
}

type postgresSecurityObservation struct {
	System   string `json:"system"`
	Data     string `json:"data"`
	Listen   string `json:"listen"`
	Port     string `json:"port"`
	Config   string `json:"config"`
	HBA      string `json:"hba"`
	Ident    string `json:"ident"`
	Password string `json:"password"`
	SSL      string `json:"ssl"`
}

func (b *postgresBackend) validSecurityObservation(j *postgresJournal, a postgresSecurityObservation) bool {
	return a.System == j.ClusterID && a.Data == b.data(j) && a.Listen == "127.0.0.1" &&
		a.Port == strconv.Itoa(j.Draft.Port) && a.Config == filepath.Join(b.configDir(j), "postgresql.conf") &&
		a.HBA == filepath.Join(b.configDir(j), "pg_hba.conf") && a.Ident == filepath.Join(b.configDir(j), "pg_ident.conf") &&
		a.Password == "scram-sha-256" && a.SSL == "off"
}

// A correct-password connection can also succeed under trust authentication.
// Require the server's specific invalid-password rejection for the same role,
// database and endpoint, instead of accepting any connection failure as proof.
func postgresRequirePassword(ctx context.Context, dsn string) error {
	parsed, e := url.Parse(dsn)
	if e != nil || parsed.User == nil {
		return ErrInput
	}
	password, present := parsed.User.Password()
	if !present || password == "" {
		return ErrInput
	}
	parsed.User = url.UserPassword(parsed.User.Username(), password+"-rixa-invalid")
	conn, e := postgresConnect(ctx, parsed.String())
	if conn != nil {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		_ = conn.Close(closeCtx)
		cancel()
		return ErrAuthority
	}
	var pgError *pgconn.PgError
	if !errors.As(e, &pgError) || pgError.Code != "28P01" {
		return ErrAuthority
	}
	return nil
}
