// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

func updateProbeCertificate(t *testing.T, names ...string) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Rixa update test"}, DNSNames: names, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pair, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	return pair, certPEM
}
func updateProbeFixture(t *testing.T, cert []byte) (*Manager, *journal) {
	t.Helper()
	m := testManager(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	j := &journal{Draft: validDraft(), CertSHA256: digest(cert)}
	j.Draft.Listen = address
	j.Draft.ControlOrigin = "https://control.rixa.test:" + port
	j.Draft.SiteOrigin = "https://site.rixa.test:" + port
	if err = writeNew(m.tlsSnapshotPath(j, false), cert, 0600); err != nil {
		t.Fatal(err)
	}
	return m, j
}
func startUpdateProbeServer(t *testing.T, address string, cert tls.Certificate, handler http.Handler) {
	t.Helper()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	stopped := make(chan error, 1)
	go func() {
		stopped <- server.Serve(tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}))
	}()
	t.Cleanup(func() {
		_ = server.Close()
		select {
		case err := <-stopped:
			if !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("test endpoint: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("test endpoint did not stop")
		}
	})
}
func assertProbeEndpointRefused(t *testing.T, address string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("fixture must start without listener: %v", err)
	}
}
func TestUpdateHTTPPostflightWaitsForDelayedTLSListener(t *testing.T) {
	pair, cert := updateProbeCertificate(t, "control.rixa.test", "site.rixa.test")
	m, j := updateProbeFixture(t, cert)
	assertProbeEndpointRefused(t, j.Draft.Listen)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	completed := make(chan error, 1)
	go func() { completed <- m.updateHTTPPostflight(ctx, j) }()
	// Before startup the former one-shot proof returned ECONNREFUSED here.
	select {
	case err := <-completed:
		t.Fatalf("did not observe listener startup: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	var mu sync.Mutex
	observed := map[string]int{}
	startUpdateProbeServer(t, j.Draft.Listen, pair, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		observed[r.Host+r.URL.Path]++
		if r.Method != http.MethodGet || r.Header.Get("Origin") != "https://"+r.Host || r.Header.Get("X-Identity-Request") != "1" || r.Header.Get("Cookie") != "" {
			t.Error("read-only probe changed origin/session boundary")
		}
		switch r.URL.Path {
		case "/admin/login":
			_, _ = io.WriteString(w, "login")
		case "/auth/csrf":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	_, port, _ := net.SplitHostPort(j.Draft.Listen)
	for _, host := range []string{"control.rixa.test:", "site.rixa.test:"} {
		for _, path := range []string{"/admin/login", "/auth/csrf"} {
			if observed[host+port+path] != 1 {
				t.Fatalf("missing exact successful probe: %+v", observed)
			}
		}
	}
	if len(observed) != 4 {
		t.Fatalf("unexpected probe paths: %+v", observed)
	}
}
func TestUpdateHTTPPostflightRejectsUnexpectedHTTPStatus(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusForbidden, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			pair, cert := updateProbeCertificate(t, "control.rixa.test", "site.rixa.test")
			m, j := updateProbeFixture(t, cert)
			var mu sync.Mutex
			requests := 0
			startUpdateProbeServer(t, j.Draft.Listen, pair, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests++
				mu.Unlock()
				w.Header().Set("Location", "/admin/login")
				w.WriteHeader(status)
			}))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := m.updateHTTPPostflight(ctx, j)
			if !errors.Is(err, ErrPrerequisite) || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("unexpected status was retried or accepted: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if requests != 1 {
				t.Fatalf("unexpected status repeated %d times", requests)
			}
		})
	}
}
func TestUpdateHTTPPostflightRejectsWrongCertificateAndHostname(t *testing.T) {
	for _, name := range []string{"untrusted-certificate", "wrong-hostname"} {
		t.Run(name, func(t *testing.T) {
			pair, cert := updateProbeCertificate(t, "control.rixa.test", "site.rixa.test")
			if name == "untrusted-certificate" {
				pair, _ = updateProbeCertificate(t, "control.rixa.test", "site.rixa.test")
			} else {
				pair, cert = updateProbeCertificate(t, "other.rixa.test")
			}
			m, j := updateProbeFixture(t, cert)
			startUpdateProbeServer(t, j.Draft.Listen, pair, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("failed TLS reached HTTP handler") }))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := m.updateHTTPPostflight(ctx, j)
			if err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("certificate failure was accepted or retried: %v", err)
			}
			if name == "untrusted-certificate" {
				var unknown x509.UnknownAuthorityError
				if !errors.As(err, &unknown) {
					t.Fatalf("certificate cause lost: %v", err)
				}
			} else {
				var hostname x509.HostnameError
				if !errors.As(err, &hostname) {
					t.Fatalf("hostname cause lost: %v", err)
				}
			}
		})
	}
}
func TestUpdateHTTPPostflightUnavailableListenerRespectsParent(t *testing.T) {
	for _, name := range []string{"deadline", "cancelled", "already-cancelled"} {
		t.Run(name, func(t *testing.T) {
			_, cert := updateProbeCertificate(t, "control.rixa.test", "site.rixa.test")
			m, j := updateProbeFixture(t, cert)
			assertProbeEndpointRefused(t, j.Draft.Listen)
			var ctx context.Context
			var cancel context.CancelFunc
			expected := context.Canceled
			if name == "deadline" {
				ctx, cancel = context.WithTimeout(context.Background(), 80*time.Millisecond)
				expected = context.DeadlineExceeded
			} else {
				ctx, cancel = context.WithCancel(context.Background())
				if name == "already-cancelled" {
					cancel()
				} else {
					timer := time.AfterFunc(80*time.Millisecond, cancel)
					defer timer.Stop()
				}
			}
			defer cancel()
			started := time.Now()
			err := m.updateHTTPPostflight(ctx, j)
			if !errors.Is(err, expected) {
				t.Fatalf("parent cancellation lost: %v", err)
			}
			if name != "already-cancelled" && !errors.Is(err, syscall.ECONNREFUSED) {
				t.Fatalf("last actual connection cause lost: %v", err)
			}
			if time.Since(started) > 2*time.Second {
				t.Fatal("parent deadline did not stop readiness wait")
			}
		})
	}
}
func TestUpdateHTTPPostflightParentCancelsInFlightResponse(t *testing.T) {
	pair, cert := updateProbeCertificate(t, "control.rixa.test", "site.rixa.test")
	m, j := updateProbeFixture(t, cert)
	entered := make(chan struct{}, 1)
	startUpdateProbeServer(t, j.Draft.Listen, pair, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-r.Context().Done() }))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := m.updateHTTPPostflight(ctx, j)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("in-flight parent deadline lost: %v", err)
	}
	select {
	case <-entered:
	default:
		t.Fatal("fixture never accepted TLS request")
	}
}
