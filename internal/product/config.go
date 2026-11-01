// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/AChWorks/achrix"
)

const (
	HostingProfileDevelopment = "development"
	HostingProfileDirectTLS   = "direct_tls"
)

var (
	ErrConfiguration     = errors.New("invalid rixa configuration")
	ErrUnavailable       = errors.New("rixa unavailable")
	ErrDatabaseAdmission = errors.New("database admission rejected")

	siteIDSyntax        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	envNameSyntax       = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,127}$`)
	accountIDSyntax     = regexp.MustCompile(`^[A-Z2-7]{26}$`)
	publicCrawlerSyntax = regexp.MustCompile(`^[A-Za-z0-9*][A-Za-z0-9._*:/-]{0,127}$`)
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

type ModuleResourceConfig struct {
	MaxConns      int32 `json:"max_conns,omitempty"`
	MaxOperations int   `json:"max_operations,omitempty"`
}

type ResourceConfig struct {
	Identity               ModuleResourceConfig `json:"identity,omitempty"`
	Audit                  ModuleResourceConfig `json:"audit,omitempty"`
	Media                  ModuleResourceConfig `json:"media,omitempty"`
	EditorialMaxOperations int                  `json:"editorial_max_operations,omitempty"`
}

type PublicCrawlerPolicy struct {
	UserAgent string `json:"user_agent"`
	Access    string `json:"access"`
	Purpose   string `json:"purpose"`
}

type PublicPolicyConfig struct {
	Indexing string                `json:"indexing"`
	Snippet  string                `json:"snippet"`
	Crawlers []PublicCrawlerPolicy `json:"crawlers"`
}

func (p PublicPolicyConfig) empty() bool {
	return p.Indexing == "" && p.Snippet == "" && len(p.Crawlers) == 0
}

func (p PublicPolicyConfig) validate() error {
	if p.Indexing != "allow" && p.Indexing != "noindex" {
		return ErrConfiguration
	}
	if p.Snippet != "allow" && p.Snippet != "none" {
		return ErrConfiguration
	}
	if len(p.Crawlers) == 0 || len(p.Crawlers) > 64 {
		return ErrConfiguration
	}
	seen := make(map[string]struct{}, len(p.Crawlers))
	hasDefault := false
	for _, crawler := range p.Crawlers {
		if !publicCrawlerSyntax.MatchString(crawler.UserAgent) {
			return ErrConfiguration
		}
		if crawler.Access != "allow" && crawler.Access != "disallow" {
			return ErrConfiguration
		}
		switch crawler.Purpose {
		case "search", "ai-search", "ai-training", "other":
		default:
			return ErrConfiguration
		}
		key := strings.ToLower(crawler.UserAgent)
		if _, duplicate := seen[key]; duplicate {
			return ErrConfiguration
		}
		seen[key] = struct{}{}
		if crawler.UserAgent == "*" {
			hasDefault = true
		}
	}
	if !hasDefault {
		return ErrConfiguration
	}
	return nil
}

type SiteConfig struct {
	ID             string             `json:"id"`
	Origin         string             `json:"origin"`
	DatabaseEnv    string             `json:"database_env"`
	MediaRoot      string             `json:"media_root"`
	PublicRoot     string             `json:"public_root,omitempty"`
	PublicPolicy   PublicPolicyConfig `json:"public_policy,omitempty"`
	AdminPrincipal string             `json:"admin_principal,omitempty"`
	Disabled       bool               `json:"disabled,omitempty"`
}

type Config struct {
	HostingProfile  string         `json:"hosting_profile,omitempty"`
	Listen          string         `json:"listen"`
	TLS             TLSConfig      `json:"tls"`
	Language        string         `json:"language,omitempty"`
	CatalogRoot     string         `json:"catalog_root,omitempty"`
	StartupTimeout  time.Duration  `json:"-"`
	ShutdownTimeout time.Duration  `json:"-"`
	Resources       ResourceConfig `json:"resources,omitempty"`
	Control         ControlConfig  `json:"control"`
	Sites           []SiteConfig   `json:"sites"`
}

type fileConfig struct {
	HostingProfile  string         `json:"hosting_profile,omitempty"`
	Listen          string         `json:"listen"`
	TLS             TLSConfig      `json:"tls"`
	Language        string         `json:"language,omitempty"`
	CatalogRoot     string         `json:"catalog_root,omitempty"`
	StartupTimeout  string         `json:"startup_timeout,omitempty"`
	ShutdownTimeout string         `json:"shutdown_timeout,omitempty"`
	Resources       ResourceConfig `json:"resources,omitempty"`
	Control         ControlConfig  `json:"control"`
	Sites           []SiteConfig   `json:"sites"`
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
	return ParseConfig(data)
}

// ParseConfig validates a single bounded configuration document. Secret values
// remain in the operator-provided environment rather than the document.
func ParseConfig(data []byte) (Config, error) {
	if len(data) == 0 || len(data) > 1<<20 {
		return Config{}, fmt.Errorf("%w: config size", ErrConfiguration)
	}
	var raw fileConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("%w: decode config", ErrConfiguration)
	}
	if err := ensureEOF(decoder); err != nil {
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
	if raw.HostingProfile == "" {
		raw.HostingProfile = HostingProfileDevelopment
	}
	c := Config{
		HostingProfile:  raw.HostingProfile,
		Listen:          raw.Listen,
		TLS:             raw.TLS,
		Language:        raw.Language,
		CatalogRoot:     raw.CatalogRoot,
		StartupTimeout:  startup,
		ShutdownTimeout: shutdown,
		Resources:       raw.Resources,
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
	if c.hostingProfile() != HostingProfileDevelopment && c.hostingProfile() != HostingProfileDirectTLS {
		return fmt.Errorf("%w: hosting profile", ErrConfiguration)
	}
	if err := c.Resources.validate(); err != nil {
		return fmt.Errorf("%w: resources", ErrConfiguration)
	}
	if len(c.Language) > 63 || strings.ContainsAny(c.Language, " \t\r\n/\\") {
		return fmt.Errorf("%w: language must identify an installed interface locale", ErrConfiguration)
	}
	if c.CatalogRoot != "" && (!filepath.IsAbs(c.CatalogRoot) || filepath.Clean(c.CatalogRoot) != c.CatalogRoot || c.CatalogRoot == "/") {
		return fmt.Errorf("%w: catalog_root must be a private absolute directory", ErrConfiguration)
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
		if site.PublicRoot == "" {
			if !site.PublicPolicy.empty() {
				return fmt.Errorf("%w: site public policy without public root", ErrConfiguration)
			}
		} else {
			if !filepath.IsAbs(site.PublicRoot) || filepath.Clean(site.PublicRoot) != site.PublicRoot || site.PublicRoot == "/" || roots[site.PublicRoot] {
				return fmt.Errorf("%w: site public root", ErrConfiguration)
			}
			if err := site.PublicPolicy.validate(); err != nil {
				return fmt.Errorf("%w: site public policy", ErrConfiguration)
			}
			roots[site.PublicRoot] = true
		}
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
	if _, err := c.loadRuntimeTLS(); err != nil {
		return err
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

func (c Config) hostingProfile() string {
	if c.HostingProfile == "" {
		return HostingProfileDevelopment
	}
	return c.HostingProfile
}

// loadRuntimeTLS returns the exact validated keypair to be served. Validation
// never binds a socket and does not establish certificate publisher trust or a
// persistent hosting service; the supported deployment profile owns those.
func (c Config) loadRuntimeTLS() (tls.Certificate, error) {
	var empty tls.Certificate
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil || host == "" {
		return empty, fmt.Errorf("%w: listener must be explicit host:port", ErrConfiguration)
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port {
		return empty, fmt.Errorf("%w: listener port", ErrConfiguration)
	}
	switch c.hostingProfile() {
	case HostingProfileDevelopment:
		if !loopbackHost(host) {
			return empty, fmt.Errorf("%w: development listener must be explicit loopback host:port", ErrConfiguration)
		}
	case HostingProfileDirectTLS:
		ip := net.ParseIP(host)
		if ip == nil || ip.IsLoopback() || ip.IsMulticast() || (!ip.IsUnspecified() && !ip.IsGlobalUnicast()) {
			return empty, fmt.Errorf("%w: direct TLS listener must be an explicit non-loopback unicast or wildcard IP", ErrConfiguration)
		}
		if err := validateDirectTLSOrigins(c, port); err != nil {
			return empty, err
		}
	default:
		return empty, fmt.Errorf("%w: hosting profile", ErrConfiguration)
	}
	if !filepath.IsAbs(c.TLS.CertFile) || !filepath.IsAbs(c.TLS.KeyFile) || c.TLS.CertFile == c.TLS.KeyFile {
		return empty, fmt.Errorf("%w: TLS file paths", ErrConfiguration)
	}
	certPEM, err := readTLSFile(c.TLS.CertFile, false)
	if err != nil {
		return empty, err
	}
	keyPEM, err := readTLSFile(c.TLS.KeyFile, true)
	if err != nil {
		return empty, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return empty, fmt.Errorf("%w: TLS certificate/key pair", ErrConfiguration)
	}
	if c.hostingProfile() == HostingProfileDirectTLS {
		if err := validateDirectTLSCertificate(c, pair, time.Now()); err != nil {
			return empty, err
		}
	}
	return pair, nil
}

func readTLSFile(path string, private bool) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: TLS file unavailable", ErrConfiguration)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 1<<20 {
		return nil, fmt.Errorf("%w: TLS file unavailable", ErrConfiguration)
	}
	if private && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: TLS private key permissions", ErrConfiguration)
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) == 0 || len(data) > 1<<20 {
		return nil, fmt.Errorf("%w: TLS file unavailable", ErrConfiguration)
	}
	return data, nil
}

func validateDirectTLSOrigins(c Config, listenPort string) error {
	check := func(raw string) error {
		if _, _, err := originParts(raw); err != nil {
			return err
		}
		u, err := url.Parse(raw)
		if err != nil || u == nil || strings.HasSuffix(u.Host, ":") {
			return ErrConfiguration
		}
		if listenPort == "443" {
			if u.Port() != "" {
				return ErrConfiguration
			}
		} else if u.Port() != listenPort {
			return ErrConfiguration
		}
		return nil
	}
	if err := check(c.Control.Origin); err != nil {
		return fmt.Errorf("%w: direct TLS control origin must match listener port", ErrConfiguration)
	}
	for _, site := range c.Sites {
		if !site.Disabled {
			if err := check(site.Origin); err != nil {
				return fmt.Errorf("%w: direct TLS site origin must match listener port", ErrConfiguration)
			}
		}
	}
	return nil
}

func validateDirectTLSCertificate(c Config, pair tls.Certificate, now time.Time) error {
	if len(pair.Certificate) == 0 {
		return fmt.Errorf("%w: direct TLS certificate leaf", ErrConfiguration)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("%w: direct TLS certificate leaf", ErrConfiguration)
	}
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return fmt.Errorf("%w: direct TLS certificate validity", ErrConfiguration)
	}
	if len(leaf.ExtKeyUsage) != 0 {
		serverAuth := false
		for _, usage := range leaf.ExtKeyUsage {
			serverAuth = serverAuth || usage == x509.ExtKeyUsageAny || usage == x509.ExtKeyUsageServerAuth
		}
		if !serverAuth {
			return fmt.Errorf("%w: direct TLS certificate server authentication", ErrConfiguration)
		}
	}
	_, controlHost, err := originParts(c.Control.Origin)
	if err != nil || leaf.VerifyHostname(controlHost) != nil {
		return fmt.Errorf("%w: direct TLS certificate control hostname", ErrConfiguration)
	}
	for _, site := range c.Sites {
		if site.Disabled {
			continue
		}
		_, hostname, err := originParts(site.Origin)
		if err != nil || leaf.VerifyHostname(hostname) != nil {
			return fmt.Errorf("%w: direct TLS certificate site hostname", ErrConfiguration)
		}
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
