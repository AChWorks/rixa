// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type addSiteTestCertificate struct {
	certPEM []byte
	keyPEM  []byte
	pair    tls.Certificate
	leaf    *x509.Certificate
	key     *ecdsa.PrivateKey
}

func addSiteTestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func addSiteTestMaterial(t *testing.T, parent *addSiteTestCertificate, hosts ...string) addSiteTestCertificate {
	t.Helper()
	key := addSiteTestKey(t)
	now := time.Now()
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: hosts[0]},
		DNSNames:     hosts,
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	issuer, signer := template, key
	if parent == nil {
		// The originally accepted server certificate is intentionally also a CA.
		// A different CA-signed leaf can then be trusted without being the
		// actual installed certificate. This reproduces the review finding.
		template.IsCA = true
		template.BasicConstraintsValid = true
		template.KeyUsage |= x509.KeyUsageCertSign
	} else {
		issuer, signer = parent.leaf, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, issuer, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pemKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if parent != nil {
		certPEM = append(certPEM, parent.certPEM...)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pemKey})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return addSiteTestCertificate{certPEM: certPEM, keyPEM: keyPEM, pair: pair, leaf: leaf, key: key}
}

func TestReadBoundAddSiteTLSFilesFailClosedOnDriftAndUnsafeSources(t *testing.T) {
	frozen := addSiteTestMaterial(t, nil, "control.rixa.test", "site-a.rixa.test", "site-b.rixa.test")
	changed := addSiteTestMaterial(t, nil, "control.rixa.test", "site-a.rixa.test")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	write := func(cert, key []byte) {
		t.Helper()
		if err := os.WriteFile(certPath, cert, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyPath, key, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(certPath, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(keyPath, 0600); err != nil {
			t.Fatal(err)
		}
	}
	check := func(name string, want bool, certHash, keyHash string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			cert, key, err := readBoundTLSFiles(certPath, keyPath, os.Geteuid(), certHash, keyHash)
			if want {
				if err != nil || !bytes.Equal(cert, frozen.certPEM) || !bytes.Equal(key, frozen.keyPEM) {
					t.Fatalf("matching files not accepted: %v", err)
				}
			} else if !errors.Is(err, ErrState) || len(cert) != 0 || len(key) != 0 {
				t.Fatalf("unsafe or changed TLS file admitted: %v", err)
			}
		})
	}
	certSHA, keySHA := digest(frozen.certPEM), digest(frozen.keyPEM)
	write(frozen.certPEM, frozen.keyPEM)
	check("matching-frozen-and-current", true, certSHA, keySHA)
	write(changed.certPEM, frozen.keyPEM)
	check("runtime-certificate-changed", false, certSHA, keySHA)
	write(frozen.certPEM, changed.keyPEM)
	check("runtime-private-key-changed", false, certSHA, keySHA)
	// Even hashes matching the bytes must not accept a non-matching pair.
	check("certificate-private-key-pair-mismatch", false, certSHA, digest(changed.keyPEM))
	write(frozen.certPEM, frozen.keyPEM)
	if err := os.Chmod(keyPath, 0644); err != nil {
		t.Fatal(err)
	}
	check("private-key-permissions", false, certSHA, keySHA)
	if err := os.Chmod(keyPath, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(certPath, 0644); err != nil {
		t.Fatal(err)
	}
	check("certificate-permissions", false, certSHA, keySHA)
	if err := os.Chmod(certPath, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(keyPath, keyPath+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(keyPath+".real", keyPath); err != nil {
		t.Fatal(err)
	}
	check("symlinked-key", false, certSHA, keySHA)
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(keyPath+".real", keyPath); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if cert, key, err := readBoundTLSFiles(certPath, filepath.Join(other, "key.pem"), os.Geteuid(), certSHA, keySHA); err == nil || cert != nil || key != nil {
		t.Fatal("split TLS directory accepted")
	}
	if cert, key, err := readBoundTLSFiles(certPath, keyPath, os.Geteuid()+1, certSHA, keySHA); err == nil || cert != nil || key != nil {
		t.Fatal("foreign file owner accepted")
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	check("directory-permissions", false, certSHA, keySHA)
}

func TestAddSiteServedTLSPinsActualLeafNotJustTrustedCA(t *testing.T) {
	original := addSiteTestMaterial(t, nil, "control.rixa.test", "site-a.rixa.test", "site-b.rixa.test")
	// This leaf is trusted by the original root and can serve the old sites,
	// yet does not cover site-b. Verify the precise reported counterexample.
	oldSitesOnly := addSiteTestMaterial(t, &original, "control.rixa.test", "site-a.rixa.test")
	// Even an alternative leaf covering site-b must fail the exact leaf pin.
	differentLeafSameSAN := addSiteTestMaterial(t, &original, "control.rixa.test", "site-a.rixa.test", "site-b.rixa.test")
	for _, tc := range []struct {
		name   string
		served addSiteTestCertificate
		accept bool
	}{
		{"exact-original-leaf", original, true},
		{"trusted-different-leaf-missing-site-b", oldSitesOnly, false},
		{"trusted-different-leaf-with-site-b", differentLeafSameSAN, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			}))
			server.TLS = &tls.Config{Certificates: []tls.Certificate{tc.served.pair}, MinVersion: tls.VersionTLS12}
			server.StartTLS()
			defer server.Close()
			err := proveServedSiteBCertificate(context.Background(), server.Listener.Addr().String(),
				"https://site-b.rixa.test:8443", original.certPEM)
			if tc.accept && err != nil {
				t.Fatalf("identical presented leaf rejected: %v", err)
			}
			if !tc.accept && !errors.Is(err, ErrPrerequisite) {
				t.Fatalf("drifted active TLS leaf accepted: %v", err)
			}
		})
	}
	if err := proveServedSiteBCertificate(context.Background(), "127.0.0.1:1",
		"https://site-b.rixa.test:8443", original.certPEM); !errors.Is(err, ErrPrerequisite) {
		t.Fatalf("inactive or wrong listener accepted: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := proveServedSiteBCertificate(cancelled, "127.0.0.1:1",
		"https://site-b.rixa.test:8443", original.certPEM); !errors.Is(err, ErrPrerequisite) {
		t.Fatalf("canceled probe accepted: %v", err)
	}
}

func TestLocalNativeEndpointMatchesEstablishedHTTPSClient(t *testing.T) {
	cases := []struct{ input, want string }{
		{"0.0.0.0:8443", "127.0.0.1:8443"},
		{"[::]:8443", "[::1]:8443"},
		{"192.0.2.1:443", "192.0.2.1:443"},
	}
	for _, tc := range cases {
		got, err := localNativeEndpoint(tc.input)
		if err != nil || got != tc.want {
			t.Fatalf("%s -> %s, %v; want %s", tc.input, got, err, tc.want)
		}
	}
	for _, input := range []string{"bad", "hostname:443", "[]:443"} {
		if _, err := localNativeEndpoint(input); !errors.Is(err, ErrInput) {
			t.Fatalf("invalid listener %q accepted", input)
		}
	}
}
