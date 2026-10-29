// SPDX-License-Identifier: MPL-2.0

package product

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func directTLSConfig(t *testing.T) Config {
	t.Helper()
	cert, key := writeDirectCertificate(t, []string{"control.rixa.test", "site.rixa.test"}, time.Now().Add(-time.Minute), time.Now().Add(time.Hour), []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	return Config{
		HostingProfile: HostingProfileDirectTLS,
		Listen:         "0.0.0.0:443", TLS: TLSConfig{CertFile: cert, KeyFile: key}, Language: "en",
		Control: ControlConfig{Origin: "https://control.rixa.test", DatabaseEnv: "RIXA_CONTROL", AdminPrincipal: testControlAdmin},
		Sites:   []SiteConfig{{ID: "site", Origin: "https://site.rixa.test", DatabaseEnv: "RIXA_SITE", MediaRoot: "/private/site", AdminPrincipal: testSiteAAdmin}},
	}
}

func TestDirectTLSListenerAndOriginBoundary(t *testing.T) {
	base := directTLSConfig(t)
	for _, test := range []struct {
		name, listen, control, site, profile string
		valid                                bool
	}{
		{name: "wildcard IPv4", listen: "0.0.0.0:443", valid: true},
		{name: "wildcard IPv6", listen: "[::]:443", valid: true},
		{name: "explicit unicast", listen: "192.0.2.10:443", valid: true},
		{name: "private unicast", listen: "10.0.0.2:443", valid: true},
		{name: "explicit port", listen: "0.0.0.0:8443", control: "https://control.rixa.test:8443", site: "https://site.rixa.test:8443", valid: true},
		{name: "loopback", listen: "127.0.0.1:443"},
		{name: "multicast", listen: "224.0.0.1:443"},
		{name: "link local", listen: "169.254.1.1:443"},
		{name: "hostname", listen: "host.example:443"},
		{name: "implicit wildcard", listen: ":443"},
		{name: "zero port", listen: "0.0.0.0:0"},
		{name: "service port", listen: "0.0.0.0:https"},
		{name: "noncanonical port", listen: "0.0.0.0:0443"},
		{name: "mismatched control", listen: "0.0.0.0:8443", site: "https://site.rixa.test:8443"},
		{name: "mismatched site", listen: "0.0.0.0:8443", control: "https://control.rixa.test:8443"},
		{name: "empty origin port", listen: "0.0.0.0:443", control: "https://control.rixa.test:"},
		{name: "explicit default origin port", listen: "0.0.0.0:443", control: "https://control.rixa.test:443"},
		{name: "development cannot expose wildcard", listen: "0.0.0.0:443", profile: HostingProfileDevelopment},
		{name: "unknown profile", listen: "0.0.0.0:443", profile: "panel"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := base
			c.Sites = append([]SiteConfig(nil), base.Sites...)
			c.Listen = test.listen
			if test.control != "" {
				c.Control.Origin = test.control
			}
			if test.site != "" {
				c.Sites[0].Origin = test.site
			}
			if test.profile != "" {
				c.HostingProfile = test.profile
			}
			err := c.ValidateRuntimeFiles()
			if test.valid && err != nil {
				t.Fatalf("valid boundary rejected: %v", err)
			}
			if !test.valid && !errors.Is(err, ErrConfiguration) {
				t.Fatalf("invalid boundary accepted: %v", err)
			}
		})
	}
	base.Sites = append(base.Sites, SiteConfig{ID: "disabled", Origin: "https://disabled.rixa.test:9999", DatabaseEnv: "RIXA_DISABLED", MediaRoot: "/private/disabled", Disabled: true})
	if err := base.ValidateRuntimeFiles(); err != nil {
		t.Fatalf("disabled site unexpectedly needs active TLS coverage: %v", err)
	}
}

func TestDirectTLSCertificateBoundary(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name          string
		hosts         []string
		before, after time.Time
		usage         []x509.ExtKeyUsage
	}{
		{name: "missing control SAN", hosts: []string{"site.rixa.test"}, before: now.Add(-time.Minute), after: now.Add(time.Hour)},
		{name: "missing site SAN", hosts: []string{"control.rixa.test"}, before: now.Add(-time.Minute), after: now.Add(time.Hour)},
		{name: "expired", hosts: []string{"control.rixa.test", "site.rixa.test"}, before: now.Add(-time.Hour), after: now.Add(-time.Minute)},
		{name: "not yet valid", hosts: []string{"control.rixa.test", "site.rixa.test"}, before: now.Add(time.Minute), after: now.Add(time.Hour)},
		{name: "client authentication only", hosts: []string{"control.rixa.test", "site.rixa.test"}, before: now.Add(-time.Minute), after: now.Add(time.Hour), usage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := directTLSConfig(t)
			cert, key := writeDirectCertificate(t, test.hosts, test.before, test.after, test.usage)
			c.TLS = TLSConfig{CertFile: cert, KeyFile: key}
			if err := c.ValidateRuntimeFiles(); !errors.Is(err, ErrConfiguration) {
				t.Fatalf("invalid certificate accepted: %v", err)
			}
		})
	}
	c := directTLSConfig(t)
	if err := os.Chmod(c.TLS.KeyFile, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateRuntimeFiles(); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("shared private key accepted: %v", err)
	}
	if err := os.Chmod(c.TLS.KeyFile, 0o600); err != nil {
		t.Fatal(err)
	}
	other := directTLSConfig(t)
	c.TLS.KeyFile = other.TLS.KeyFile
	if err := c.ValidateRuntimeFiles(); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("unmatched private key accepted: %v", err)
	}
	pipe := filepath.Join(t.TempDir(), "key-pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	c.TLS.KeyFile = pipe
	if err := c.ValidateRuntimeFiles(); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("non-regular key accepted: %v", err)
	}
}

func TestParseConfigProfileDefaultsAndStrictDocument(t *testing.T) {
	data := `{"listen":"127.0.0.1:8443","tls":{"cert_file":"/private/cert","key_file":"/private/key"},"control":{"origin":"https://control.test:8443","database_env":"RIXA_CONTROL"},"sites":[{"id":"a","origin":"https://a.test:8443","database_env":"RIXA_A","media_root":"/private/a"}]}`
	c, err := ParseConfig([]byte(data))
	if err != nil || c.HostingProfile != HostingProfileDevelopment {
		t.Fatalf("development default missing: %q %v", c.HostingProfile, err)
	}
	for _, invalid := range []string{data + `{}`, strings.Replace(data, `"listen":`, `"hosting_profile":"panel","listen":`, 1), strings.Repeat(" ", (1<<20)+1)} {
		if _, err := ParseConfig([]byte(invalid)); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("invalid document accepted: %v", err)
		}
	}
}

func writeDirectCertificate(t *testing.T, hosts []string, before, after time.Time, usage []x509.ExtKeyUsage) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: before, NotAfter: after, DNSNames: hosts, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usage}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
