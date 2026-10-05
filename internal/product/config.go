// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/AChWorks/achrix"
)

var (
	ErrConfiguration = errors.New("invalid rixa configuration")
	ErrUnavailable   = errors.New("rixa unavailable")

	siteIDSyntax    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	envNameSyntax   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,127}$`)
	accountIDSyntax = regexp.MustCompile(`^[A-Z2-7]{26}$`)
)

type TLSConfig struct {
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
}

type ControlConfig struct {
	Origin         string `json:"origin"`
	DatabaseEnv    string `json:"database_env"`
	AdminPrincipal string `json:"admin_principal,omitempty"`
}

type SiteConfig struct {
	ID             string `json:"id"`
	Origin         string `json:"origin"`
	DatabaseEnv    string `json:"database_env"`
	MediaRoot      string `json:"media_root"`
	AdminPrincipal string `json:"admin_principal,omitempty"`
	Disabled       bool   `json:"disabled,omitempty"`
}

type Config struct {
	Listen          string        `json:"listen"`
	TLS             TLSConfig     `json:"tls"`
	Language        string        `json:"language,omitempty"`
	StartupTimeout  time.Duration `json:"-"`
	ShutdownTimeout time.Duration `json:"-"`
	Control         ControlConfig `json:"control"`
	Sites           []SiteConfig  `json:"sites"`
}

type fileConfig struct {
	Listen          string        `json:"listen"`
	TLS             TLSConfig     `json:"tls"`
	Language        string        `json:"language,omitempty"`
	StartupTimeout  string        `json:"startup_timeout,omitempty"`
	ShutdownTimeout string        `json:"shutdown_timeout,omitempty"`
	Control         ControlConfig `json:"control"`
	Sites           []SiteConfig  `json:"sites"`
}

type ResolvedControl struct {
	ControlConfig
	DSN string
}

type ResolvedSite struct {
	SiteConfig
	DSN string
}

type RuntimeConfig struct {
	Config
	Control ResolvedControl
	Sites   []ResolvedSite
}

func LoadConfig(path string) (Config, error) {
	if path == "" || !filepath.IsAbs(path) {
		return Config{}, fmt.Errorf("%w: config path must be absolute", ErrConfiguration)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("%w: read config", ErrConfiguration)
	}
	if len(data) == 0 || len(data) > 1<<20 {
		return Config{}, fmt.Errorf("%w: config size", ErrConfiguration)
	}
	var raw fileConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("%w: decode config", ErrConfiguration)
	}
	if err = ensureEOF(decoder); err != nil {
		return Config{}, err
	}

	startup, err := boundedDuration(raw.StartupTimeout, 5*time.Second)
	if err != nil {
		return Config{}, fmt.Errorf("%w: startup_timeout", ErrConfiguration)
	}
	shutdown, err := boundedDuration(raw.ShutdownTimeout, 5*time.Second)
	if err != nil {
		return Config{}, fmt.Errorf("%w: shutdown_timeout", ErrConfiguration)
	}
	if raw.Language == "" {
		raw.Language = "en"
	}
	c := Config{
		Listen:          raw.Listen,
		TLS:             raw.TLS,
		Language:        raw.Language,
		StartupTimeout:  startup,
		ShutdownTimeout: shutdown,
		Control:         raw.Control,
		Sites:           append([]SiteConfig(nil), raw.Sites...),
	}
	if err = c.validateStructure(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return fmt.Errorf("%w: trailing config data", ErrConfiguration)
}

func boundedDuration(value string, fallback time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d < time.Second || d > time.Minute {
		return 0, ErrConfiguration
	}
	return d, nil
}

func (c Config) validateStructure() error {
	if c.Language != "en" && c.Language != "fa" {
		return fmt.Errorf("%w: language", ErrConfiguration)
	}
	controlAuthority, controlHostname, err := originParts(c.Control.Origin)
	if err != nil || !envNameSyntax.MatchString(c.Control.DatabaseEnv) {
		return fmt.Errorf("%w: control", ErrConfiguration)
	}
	if c.Control.AdminPrincipal != "" && !accountIDSyntax.MatchString(c.Control.AdminPrincipal) {
		return fmt.Errorf("%w: control admin principal", ErrConfiguration)
	}
	if len(c.Sites) == 0 {
		return fmt.Errorf("%w: site inventory", ErrConfiguration)
	}

	authorities := map[string]bool{controlAuthority: true}
	hostnames := map[string]bool{controlHostname: true}
	ids := make(map[string]bool, len(c.Sites))
	dbEnvs := map[string]bool{c.Control.DatabaseEnv: true}
	roots := make(map[string]bool, len(c.Sites))
	for _, site := range c.Sites {
		if !siteIDSyntax.MatchString(site.ID) || ids[site.ID] {
			return fmt.Errorf("%w: site identity", ErrConfiguration)
		}
		ids[site.ID] = true
		authority, hostname, e := originParts(site.Origin)
		if e != nil || authorities[authority] || hostnames[hostname] {
			return fmt.Errorf("%w: site origin", ErrConfiguration)
		}
		authorities[authority] = true
		hostnames[hostname] = true
		if !envNameSyntax.MatchString(site.DatabaseEnv) || dbEnvs[site.DatabaseEnv] {
			return fmt.Errorf("%w: site database reference", ErrConfiguration)
		}
		dbEnvs[site.DatabaseEnv] = true
		if site.AdminPrincipal != "" && !accountIDSyntax.MatchString(site.AdminPrincipal) {
			return fmt.Errorf("%w: site admin principal", ErrConfiguration)
		}
		if !filepath.IsAbs(site.MediaRoot) || filepath.Clean(site.MediaRoot) != site.MediaRoot || site.MediaRoot == "/" || roots[site.MediaRoot] {
			return fmt.Errorf("%w: site media root", ErrConfiguration)
		}
		roots[site.MediaRoot] = true
	}
	return nil
}

func originParts(raw string) (authority, hostname string, err error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.String() != raw {
		return "", "", ErrConfiguration
	}
	if strings.ContainsAny(u.Host, "\r\n\t ") {
		return "", "", ErrConfiguration
	}
	hostname = strings.ToLower(u.Hostname())
	if hostname == "" {
		return "", "", ErrConfiguration
	}
	return u.Host, hostname, nil
}

func originAuthority(raw string) (string, error) {
	authority, _, err := originParts(raw)
	return authority, err
}

func (c Config) ValidateRuntimeFiles() error {
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil || !loopbackHost(host) {
		return fmt.Errorf("%w: development listener must be explicit loopback host:port", ErrConfiguration)
	}
	if !filepath.IsAbs(c.TLS.CertFile) || !filepath.IsAbs(c.TLS.KeyFile) || c.TLS.CertFile == c.TLS.KeyFile {
		return fmt.Errorf("%w: TLS file paths", ErrConfiguration)
	}
	for _, path := range []string{c.TLS.CertFile, c.TLS.KeyFile} {
		info, e := os.Stat(path)
		if e != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: TLS file unavailable", ErrConfiguration)
		}
	}
	keyInfo, err := os.Stat(c.TLS.KeyFile)
	if err != nil || keyInfo.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: TLS private key permissions", ErrConfiguration)
	}
	if _, err = tls.LoadX509KeyPair(c.TLS.CertFile, c.TLS.KeyFile); err != nil {
		return fmt.Errorf("%w: TLS certificate/key pair", ErrConfiguration)
	}
	if !accountIDSyntax.MatchString(c.Control.AdminPrincipal) {
		return fmt.Errorf("%w: control admin principal required", ErrConfiguration)
	}
	active := 0
	for _, site := range c.Sites {
		if !site.Disabled {
			active++
		}
		if !site.Disabled && !accountIDSyntax.MatchString(site.AdminPrincipal) {
			return fmt.Errorf("%w: enabled site admin principal required", ErrConfiguration)
		}
	}
	if active == 0 {
		return fmt.Errorf("%w: at least one enabled site required", ErrConfiguration)
	}
	return nil
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c Config) ResolveRuntime(parent context.Context, getenv func(string) (string, bool)) (RuntimeConfig, error) {
	return c.resolveRuntime(parent, getenv, validateRuntimeTargets)
}

type runtimeValidator func(context.Context, *RuntimeConfig) error

func (c Config) resolveRuntime(parent context.Context, getenv func(string) (string, bool), validate runtimeValidator) (RuntimeConfig, error) {
	if getenv == nil {
		getenv = os.LookupEnv
	}
	if err := c.ValidateRuntimeFiles(); err != nil {
		return RuntimeConfig{}, err
	}
	controlDSN, ok := getenv(c.Control.DatabaseEnv)
	if !ok || controlDSN == "" {
		return RuntimeConfig{}, fmt.Errorf("%w: missing database secret %s", ErrConfiguration, c.Control.DatabaseEnv)
	}
	result := RuntimeConfig{
		Config:  c,
		Control: ResolvedControl{ControlConfig: c.Control, DSN: controlDSN},
		Sites:   make([]ResolvedSite, 0, len(c.Sites)),
	}
	for _, site := range c.Sites {
		resolved := ResolvedSite{SiteConfig: site}
		if !site.Disabled {
			dsn, exists := getenv(site.DatabaseEnv)
			if !exists || dsn == "" {
				return RuntimeConfig{}, fmt.Errorf("%w: missing database secret %s", ErrConfiguration, site.DatabaseEnv)
			}
			resolved.DSN = dsn
		}
		result.Sites = append(result.Sites, resolved)
	}
	if validate != nil {
		if err := validate(parent, &result); err != nil {
			return RuntimeConfig{}, err
		}
	}
	return result, nil
}

func (c Config) DatabaseTargets(parent context.Context, getenv func(string) (string, bool)) ([]ResolvedDatabase, error) {
	if getenv == nil {
		getenv = os.LookupEnv
	}
	targets := make([]ResolvedDatabase, 0, len(c.Sites)+1)
	control, ok := getenv(c.Control.DatabaseEnv)
	if !ok || control == "" {
		return nil, fmt.Errorf("%w: missing database secret %s", ErrConfiguration, c.Control.DatabaseEnv)
	}
	targets = append(targets, ResolvedDatabase{Kind: DatabaseControl, ID: "control", DSN: control})
	for _, site := range c.Sites {
		if site.Disabled {
			continue
		}
		dsn, exists := getenv(site.DatabaseEnv)
		if !exists || dsn == "" {
			return nil, fmt.Errorf("%w: missing database secret %s", ErrConfiguration, site.DatabaseEnv)
		}
		targets = append(targets, ResolvedDatabase{Kind: DatabaseSite, ID: site.ID, DSN: dsn})
	}
	if err := validateDatabaseTargets(parent, targets); err != nil {
		return nil, err
	}
	return targets, nil
}

type DatabaseKind uint8

const (
	DatabaseControl DatabaseKind = iota + 1
	DatabaseSite
)

type ResolvedDatabase struct {
	Kind DatabaseKind
	ID   string
	DSN  string
}

func (c Config) BootstrapDatabase(scope string, getenv func(string) (string, bool)) (ResolvedDatabase, error) {
	if getenv == nil {
		getenv = os.LookupEnv
	}
	if scope == "control" {
		dsn, ok := getenv(c.Control.DatabaseEnv)
		if !ok || dsn == "" {
			return ResolvedDatabase{}, fmt.Errorf("%w: missing database secret %s", ErrConfiguration, c.Control.DatabaseEnv)
		}
		return ResolvedDatabase{Kind: DatabaseControl, ID: "control", DSN: dsn}, nil
	}
	const prefix = "site:"
	if !strings.HasPrefix(scope, prefix) {
		return ResolvedDatabase{}, fmt.Errorf("%w: bootstrap scope", ErrConfiguration)
	}
	id := strings.TrimPrefix(scope, prefix)
	for _, site := range c.Sites {
		if site.ID != id || site.Disabled {
			continue
		}
		dsn, ok := getenv(site.DatabaseEnv)
		if !ok || dsn == "" {
			return ResolvedDatabase{}, fmt.Errorf("%w: missing database secret %s", ErrConfiguration, site.DatabaseEnv)
		}
		return ResolvedDatabase{Kind: DatabaseSite, ID: site.ID, DSN: dsn}, nil
	}
	return ResolvedDatabase{}, fmt.Errorf("%w: bootstrap scope", ErrConfiguration)
}

func Principal(value string) (achrix.Principal, error) {
	if !accountIDSyntax.MatchString(value) {
		return "", ErrConfiguration
	}
	return achrix.Principal(value), nil
}
