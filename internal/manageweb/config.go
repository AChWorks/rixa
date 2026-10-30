// SPDX-License-Identifier: MPL-2.0
// Package manageweb presents one temporary infrastructure-operator operation.
// Native facades remain the sole effect, journal and recovery owners.
package manageweb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	shell "github.com/AChWorks/achrix/admin"
	"github.com/AChWorks/achrix/admin/localize"
	"github.com/AChWorks/rixa/internal/releaseartifact"
	"github.com/AChWorks/rixa/internal/serverinstall"
)

var ErrConfiguration = errors.New("invalid temporary management console configuration")
var operationID = regexp.MustCompile("^[a-z0-9][a-z0-9-]{0,47}$")

const domain = "rixa-manager"
const capability = "rixa.console.operation"
const principal = "rixa.console.operator"

// Scope is issued by the local infrastructure operator, never by a browser.
type Scope struct {
	Kind         string
	Operation    string
	Installation string
	Actions      []string
}
type Options struct {
	Listen             string
	Certificate        string
	Key                string
	RuntimeCertificate string
	RuntimeKey         string
	PermitFile         string
	StorageRoot        string
	CatalogRoot        string
	Lifetime           time.Duration
	Scope              Scope
	ManagerVersion     string
}
type dependencies struct {
	owner  int
	strict bool
	now    func() time.Time
	fetch  func(context.Context, releaseartifact.FetchRequest) (releaseartifact.FetchResult, error)
	verify func(context.Context, releaseartifact.Request) (releaseartifact.Result, error)
}

func New(options Options, backend serverinstall.Facade) (*Console, error) {
	if os.Geteuid() != 0 {
		return nil, serverinstall.ErrAuthority
	}
	return newConsole(options, backend, dependencies{0, true, time.Now, releaseartifact.Fetch, releaseartifact.Verify})
}
func privatePath(path string, owner int, directory, strict bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsAny(path, "\x00\r\n") {
		return ErrConfiguration
	}
	actual, e := filepath.EvalSymlinks(path)
	if e != nil || actual != path {
		return ErrConfiguration
	}
	info, e := os.Lstat(path)
	if e != nil {
		return ErrConfiguration
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != owner || info.Mode().Perm()&0077 != 0 || directory != info.IsDir() || (!directory && (!info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 1<<20)) {
		return ErrConfiguration
	}
	if strict {
		for p := filepath.Dir(path); ; p = filepath.Dir(p) {
			st, e := os.Lstat(p)
			if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0022 != 0 {
				return ErrConfiguration
			}
			raw, ok := st.Sys().(*syscall.Stat_t)
			if !ok || raw.Uid != 0 {
				return ErrConfiguration
			}
			if p == "/" {
				break
			}
		}
	}
	return nil
}
func validate(options Options) (string, error) {
	host, port, e := net.SplitHostPort(options.Listen)
	n, parse := strconv.Atoi(port)
	if e != nil || parse != nil || host != "127.0.0.1" || n < 1024 || n > 65535 || strconv.Itoa(n) != port {
		return "", ErrConfiguration
	}
	s := options.Scope
	if !operationID.MatchString(s.Operation) || (s.Kind != "install" && s.Kind != "update") ||
		(s.Kind == "update" && (!operationID.MatchString(s.Installation) || s.Installation == s.Operation)) ||
		(s.Kind == "install" && s.Installation != "") || options.Lifetime < time.Minute || options.Lifetime > 15*time.Minute {
		return "", ErrConfiguration
	}
	validActions := map[string]bool{"configure": true, "review": true, "apply": true, "resume": true, "status": true, "remove": true}
	seen := map[string]bool{}
	for _, a := range s.Actions {
		if seen[a] || !validActions[a] {
			return "", ErrConfiguration
		}
		seen[a] = true
	}
	if !seen["status"] || len(seen) > 6 {
		return "", ErrConfiguration
	}
	return "https://" + options.Listen, nil
}
func newConsole(options Options, backend serverinstall.Facade, deps dependencies) (*Console, error) {
	origin, e := validate(options)
	if e != nil || backend == nil || deps.now == nil || deps.fetch == nil || deps.verify == nil {
		return nil, ErrConfiguration
	}
	if e = privatePath(filepath.Dir(options.PermitFile), deps.owner, true, deps.strict); e != nil {
		return nil, fmt.Errorf("%w: permit directory", e)
	}
	if e = privatePath(options.StorageRoot, deps.owner, true, deps.strict); e != nil {
		return nil, fmt.Errorf("%w: artifact storage directory", e)
	}
	for _, path := range []string{options.Certificate, options.Key} {
		if e = privatePath(path, deps.owner, false, deps.strict); e != nil {
			return nil, fmt.Errorf("%w: console TLS files", e)
		}
	}
	if options.Scope.Kind == "install" {
		for _, path := range []string{options.RuntimeCertificate, options.RuntimeKey} {
			if e = privatePath(path, deps.owner, false, deps.strict); e != nil {
				return nil, e
			}
		}
	}
	pair, e := tls.LoadX509KeyPair(options.Certificate, options.Key)
	if e != nil || len(pair.Certificate) == 0 {
		return nil, ErrConfiguration
	}
	cert, e := x509.ParseCertificate(pair.Certificate[0])
	if e != nil || cert.VerifyHostname("127.0.0.1") != nil || deps.now().Before(cert.NotBefore) || !deps.now().Before(cert.NotAfter) {
		return nil, fmt.Errorf("%w: console TLS validity/loopback hostname", ErrConfiguration)
	}
	sources := []fs.FS{shell.Catalogs(), catalogs()}
	if options.CatalogRoot != "" {
		if e = privatePath(options.CatalogRoot, deps.owner, true, deps.strict); e != nil {
			return nil, e
		}
		sources = append(sources, os.DirFS(options.CatalogRoot))
	}
	catalog, e := localize.Load(sources...)
	if e != nil {
		return nil, fmt.Errorf("%w: catalogs: %w", ErrConfiguration, e)
	}
	raw := make([]byte, 32)
	if _, e = rand.Read(raw); e != nil {
		return nil, e
	}
	permit := base64.RawURLEncoding.EncodeToString(raw)
	dir, e := os.MkdirTemp(options.StorageRoot, "console-")
	if e != nil {
		return nil, e
	}
	c := &Console{options: options, origin: origin, backend: backend, deps: deps, catalog: catalog, stage: dir, permitHash: sha256.Sum256([]byte(permit)), expires: deps.now().Add(options.Lifetime), slots: make(chan struct{}, 4)}
	c.updater, _ = backend.(serverinstall.UpdateFacade)
	if options.Scope.Kind == "update" && c.updater == nil {
		return nil, ErrConfiguration
	}
	if _, e = rand.Read(c.csrfKey[:]); e != nil {
		return nil, e
	}
	suffix := make([]byte, 8)
	if _, e = rand.Read(suffix); e != nil {
		return nil, e
	}
	c.cookieName = "__Host-Rixa-Console-" + fmt.Sprintf("%x", suffix)
	c.allowed = map[string]bool{}
	for _, a := range options.Scope.Actions {
		c.allowed[a] = true
	}
	c.shell, e = shell.New(shell.Config{Origin: origin, AuthPath: "/console-auth", Language: "en", Catalogs: sources[1:], SelectLanguage: c.language}, c, c,
		shell.Surface{ID: "operation", Title: shell.Text{Domain: domain, Key: "title"}, Capability: capability, Target: c.target(), Handler: c.surface})
	if e != nil {
		return nil, e
	}
	f, e := os.OpenFile(options.PermitFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	if e = f.Chmod(0600); e == nil {
		_, e = f.WriteString(permit + "\n")
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil || closeErr != nil {
		return nil, errors.Join(e, closeErr)
	}
	c.permitInfo, e = os.Lstat(options.PermitFile)
	if e != nil {
		return nil, e
	}
	return c, nil
}
func (c *Console) target() string        { return c.options.Scope.Kind + ":" + c.options.Scope.Operation }
func (c *Console) Origin() string        { return c.origin }
func (c *Console) Handler() http.Handler { return http.HandlerFunc(c.serve) }

// Close revokes access only. Staged inputs are retained for independent executors.
func (c *Console) Close() error {
	c.mutex.Lock()
	c.closed = true
	c.sessionHash = [32]byte{}
	c.mutex.Unlock()
	info, e := os.Lstat(c.options.PermitFile)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil || !os.SameFile(info, c.permitInfo) || !info.Mode().IsRegular() {
		return ErrConfiguration
	}
	b, e := os.ReadFile(c.options.PermitFile)
	if e != nil || sha256.Sum256([]byte(strings.TrimSuffix(string(b), "\n"))) != c.permitHash {
		return ErrConfiguration
	}
	return os.Remove(c.options.PermitFile)
}

// Serve starts only the declared loopback TLS listener. It stops the console,
// never its separately supervised native operation.
func (c *Console) Serve(ctx context.Context, ready func(string)) error {
	listener, e := net.Listen("tcp", c.options.Listen)
	if e != nil {
		return errors.Join(e, c.Close())
	}
	server := &http.Server{Handler: c.Handler(), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 3 * time.Minute, WriteTimeout: 3 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
	done := make(chan error, 1)
	go func() { done <- server.ServeTLS(listener, c.options.Certificate, c.options.Key) }()
	if ready != nil {
		ready(c.origin + "/access")
	}
	timer := time.NewTimer(time.Until(c.expires))
	defer timer.Stop()
	select {
	case e = <-done:
	case <-ctx.Done():
	case <-timer.C:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopErr := server.Shutdown(shutdown)
	closeErr := c.Close()
	if errors.Is(e, http.ErrServerClosed) {
		e = nil
	}
	return errors.Join(e, stopErr, closeErr)
}

// ValidateOptions checks command/schema shape without creating files or binding.
func ValidateOptions(options Options) error {
	_, err := validate(options)
	if err != nil {
		return err
	}
	for _, path := range []string{options.PermitFile, options.StorageRoot, options.Certificate, options.Key} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsAny(path, "\x00\r\n") {
			return ErrConfiguration
		}
	}
	if options.Scope.Kind == "install" {
		for _, path := range []string{options.RuntimeCertificate, options.RuntimeKey} {
			if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsAny(path, "\x00\r\n") {
				return ErrConfiguration
			}
		}
	}
	return nil
}

// PrepareStorage creates only this declared private root, never a hierarchy or
// an existing unrelated directory. It grants no execution authority.
func PrepareStorage(path string) error {
	if os.Geteuid() != 0 {
		return serverinstall.ErrAuthority
	}
	if e := privatePath(path, 0, true, true); e == nil {
		return nil
	}
	if _, e := os.Lstat(path); !os.IsNotExist(e) {
		return ErrConfiguration
	}
	parent := filepath.Dir(path)
	actual, e := filepath.EvalSymlinks(parent)
	if e != nil || actual != parent {
		return ErrConfiguration
	}
	for p := parent; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return ErrConfiguration
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return ErrConfiguration
		}
		if p == "/" {
			break
		}
	}
	if filepath.Clean(path) != path || !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return ErrConfiguration
	}
	if e = os.Mkdir(path, 0700); e != nil {
		return e
	}
	return privatePath(path, 0, true, true)
}
