// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

// PostgreSQL pg_authid SCRAM storage (RFC5803) with SHA-256 (RFC7677).
// https://www.postgresql.org/docs/18/catalog-pg-authid.html
// Generated
// passwords are ASCII random hex, so no user-password normalization is involved.
// The client-generated verifier keeps plaintext out of SQL/server statement logs.
func postgresSCRAMVerifier(password string, salt []byte) (string, error) {
	if !hashSyntax.MatchString(password) || len(salt) != 16 {
		return "", ErrInput
	}
	salted := pbkdf2.Key([]byte(password), salt, 4096, 32, sha256.New)
	mac := hmac.New(sha256.New, salted)
	_, _ = mac.Write([]byte("Client Key"))
	stored := sha256.Sum256(mac.Sum(nil))
	mac = hmac.New(sha256.New, salted)
	_, _ = mac.Write([]byte("Server Key"))
	server := mac.Sum(nil)
	encode := base64.StdEncoding.EncodeToString
	return "SCRAM-SHA-256$4096:" + encode(salt) + "$" + encode(stored[:]) + ":" + encode(server), nil
}
func newPostgresVerifier(password string) (string, error) {
	salt := make([]byte, 16)
	if _, e := rand.Read(salt); e != nil {
		return "", e
	}
	return postgresSCRAMVerifier(password, salt)
}
func validPostgresVerifier(password, verifier string) bool {
	parts := strings.Split(verifier, "$")
	if len(parts) != 3 || parts[0] != "SCRAM-SHA-256" {
		return false
	}
	count, salt64, ok := strings.Cut(parts[1], ":")
	if !ok || count != "4096" {
		return false
	}
	salt, e := base64.StdEncoding.Strict().DecodeString(salt64)
	if e != nil || len(salt) != 16 {
		return false
	}
	expected, e := postgresSCRAMVerifier(password, salt)
	return e == nil && subtle.ConstantTimeCompare([]byte(expected), []byte(verifier)) == 1
}

func postgresRoleCreationSQL(j *postgresJournal, scope string) (string, error) {
	name := postgresRoleName(j, scope)
	if name == "" {
		return "", ErrInput
	}
	password, verifier := j.ControlPassword, j.ControlVerifier
	if scope == "site" {
		password, verifier = j.SitePassword, j.SiteVerifier
	}
	if !validPostgresVerifier(password, verifier) {
		return "", ErrInput
	}
	return "BEGIN; SET LOCAL log_statement='none'; SET LOCAL log_min_error_statement='panic'; SET LOCAL password_encryption='scram-sha-256'; CREATE ROLE \"" + name + "\" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '" + verifier + "'; COMMENT ON ROLE \"" + name + "\" IS '" + postgresOwnership(j) + "'; COMMIT;", nil
}
