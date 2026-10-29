// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/AChWorks/achrix"
)

type gatedServeModule struct {
	*lifecycleProbe
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *gatedServeModule) Ready(ctx context.Context) error {
	m.once.Do(func() { close(m.entered) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.release:
		return m.lifecycleProbe.Ready(ctx)
	}
}

func serveProbeRuntime(t *testing.T, address string, module achrix.Module) (*Runtime, *tls.Config) {
	t.Helper()
	policy := achrix.PolicyFunc(func(context.Context, achrix.Principal, string, string) error { return achrix.ErrDenied })
	app, err := achrix.New(achrix.Config{StartupTimeout: 3 * time.Second, ShutdownTimeout: time.Second}, policy, module)
	if err != nil {
		t.Fatal(err)
	}
	cert, key, roots := writeTestCertificate(t, t.TempDir(), []string{"localhost"})
	runtime := &Runtime{
		Config:       RuntimeConfig{Config: Config{Listen: address, TLS: TLSConfig{CertFile: cert, KeyFile: key}, StartupTimeout: 3 * time.Second, ShutdownTimeout: time.Second}},
		Handler:      http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
		applications: []*achrix.Application{app}, Sites: map[string]*SiteRuntime{},
	}
	return runtime, &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12}
}

func TestServeWaitsForReadinessAndUsesValidatedTLSBytes(t *testing.T) {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(freePort(t)))
	module := &gatedServeModule{lifecycleProbe: &lifecycleProbe{id: "rixa.probe.serve-ready"}, entered: make(chan struct{}), release: make(chan struct{})}
	runtime, roots := serveProbeRuntime(t, address, module)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- Serve(ctx, runtime, nil) }()
	finished := false
	defer func() {
		cancel()
		if !finished {
			select {
			case <-result:
			case <-time.After(4 * time.Second):
				t.Error("Serve did not finish bounded cleanup")
			}
		}
	}()
	select {
	case <-module.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not reach readiness")
	}
	if conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("ingress bound before readiness")
	}
	// Replace both paths after preflight. The server must use its validated
	// keypair, rather than reopen an unverified file after starting resources.
	if err := os.WriteFile(runtime.Config.TLS.CertFile, []byte("replaced certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtime.Config.TLS.KeyFile, []byte("replaced key"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(module.release)
	transport := &http.Transport{TLSClientConfig: roots}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, err := client.Get("https://" + address + "/")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				t.Fatalf("HTTPS status = %d", response.StatusCode)
			}
			break
		}
		select {
		case serveErr := <-result:
			finished = true
			t.Fatalf("Serve exited: %v", serveErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("validated TLS ingress never became ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-result:
		finished = true
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not stop")
	}
	started, stopped := module.state()
	if !started || !stopped || runtime.started {
		t.Fatalf("runtime lifecycle was not cleaned: started=%v stopped=%v", started, stopped)
	}
}

func TestServeBindFailurePreservesListenerAndStopsRuntime(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	module := &lifecycleProbe{id: "rixa.probe.serve-bind"}
	runtime, _ := serveProbeRuntime(t, occupied.Addr().String(), module)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := Serve(ctx, runtime, nil); err == nil {
		t.Fatal("occupied listener accepted")
	}
	started, stopped := module.state()
	if !started || !stopped || runtime.started {
		t.Fatalf("bind failure leaked runtime: started=%v stopped=%v", started, stopped)
	}
	conn, err := net.DialTimeout("tcp", occupied.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("unrelated listener was changed: %v", err)
	}
	_ = conn.Close()
}

func TestServeStartupFailureNeverBinds(t *testing.T) {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(freePort(t)))
	module := &lifecycleProbe{id: "rixa.probe.serve-start", fail: true}
	runtime, _ := serveProbeRuntime(t, address, module)
	if err := Serve(context.Background(), runtime, nil); err == nil {
		t.Fatal("startup failure ignored")
	}
	if conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("listener bound despite failed startup")
	}
	_, stopped := module.state()
	if !stopped {
		t.Fatal("failed startup was not cleaned")
	}
	if err := runtime.applications[0].Ready(context.Background()); !errors.Is(err, achrix.ErrNotReady) {
		t.Fatalf("failed runtime remained ready: %v", err)
	}
}
