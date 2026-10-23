package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/tx-submit-api-mirror/internal/config"
	"github.com/fxamacker/cbor/v2"
	maestro "github.com/maestro-org/go-sdk/client"
)

// testTx returns a structurally valid Conway transaction and its hash. It is
// not valid on chain; the service only parses it and forwards the bytes.
func testTx(t *testing.T) ([]byte, string) {
	t.Helper()
	addr := append([]byte{0x60}, make([]byte, 28)...)
	body := map[uint]any{
		0: [][]any{{make([]byte, 32), uint32(0)}},
		1: []map[uint]any{{0: addr, 1: uint64(1_000_000)}},
		2: uint64(100_000),
	}
	raw, err := cbor.Marshal([]any{body, map[uint]any{}, true, nil})
	if err != nil {
		t.Fatalf("encode tx: %v", err)
	}
	txType, err := ledger.DetermineTransactionType(raw)
	if err != nil {
		t.Fatalf("fixture is not a recognizable transaction: %v", err)
	}
	tx, err := ledger.NewTransactionFromCbor(txType, raw)
	if err != nil {
		t.Fatalf("fixture does not parse: %v", err)
	}
	return raw, tx.Hash().String()
}

// withConfig swaps the global config for one test. handleSubmitTx reads the
// global, so the original must be restored for the other tests.
func withConfig(t *testing.T, mutate func(*config.Config)) {
	t.Helper()
	cfg := config.GetConfig()
	saved := *cfg
	mutate(cfg)
	t.Cleanup(func() { *cfg = saved })
}

type received struct {
	method, path, contentType, apiKey string
	body                              []byte
}

// recordingServer answers every request with status and reports what it saw.
// Submission to backends is asynchronous, so tests wait on the channel.
func recordingServer(t *testing.T, status int, respBody string) (*httptest.Server, <-chan received) {
	t.Helper()
	ch := make(chan received, 4)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			ch <- received{
				method:      r.Method,
				path:        r.URL.Path,
				contentType: r.Header.Get("Content-Type"),
				apiKey:      r.Header.Get("api-key"),
				body:        body,
			}
			w.WriteHeader(status)
			_, _ = io.WriteString(w, respBody)
		},
	))
	t.Cleanup(srv.Close)
	return srv, ch
}

// waitFor returns the next request the recording server saw, failing the test
// if none arrives, since backend submissions happen in goroutines.
func waitFor(t *testing.T, ch <-chan received) received {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the submission to reach the server")
		return received{}
	}
}

// nilLogger returns a logger that discards output, for tests that do not
// inspect the access log.
func nilLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// submit POSTs raw to /api/submit/tx on a fresh handler and returns the response.
func submit(t *testing.T, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	newHandler(nilLogger()).ServeHTTP(w, httptest.NewRequest(
		http.MethodPost, "/api/submit/tx", strings.NewReader(string(raw))))
	return w
}

// The caller gets the transaction hash with 202 immediately, and every backend
// receives the unmodified CBOR as application/cbor.
func TestSubmitTxForwardsToBackends(t *testing.T) {
	raw, hash := testTx(t)
	b1, ch1 := recordingServer(t, http.StatusAccepted, "")
	b2, ch2 := recordingServer(t, http.StatusAccepted, "")
	withConfig(t, func(c *config.Config) {
		c.Backends = []string{b1.URL, b2.URL}
	})

	w := submit(t, raw)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want 202", w.Code)
	}
	if w.Body.String() != hash {
		t.Errorf("body: got %q, want hash %q", w.Body.String(), hash)
	}
	if got := w.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("content-type: got %q", got)
	}
	for i, ch := range []<-chan received{ch1, ch2} {
		r := waitFor(t, ch)
		if r.method != http.MethodPost || r.contentType != "application/cbor" {
			t.Errorf("backend %d: got %s %q", i, r.method, r.contentType)
		}
		if string(r.body) != string(raw) {
			t.Errorf("backend %d: body was not forwarded unmodified", i)
		}
	}
}

// A backend rejecting the transaction is logged server side only: the caller
// has already been answered, and the other backends are unaffected.
func TestSubmitTxBackendRejectionDoesNotFailRequest(t *testing.T) {
	raw, hash := testTx(t)
	bad, badCh := recordingServer(t, http.StatusBadRequest, "ledger rejected")
	good, goodCh := recordingServer(t, http.StatusAccepted, "")
	withConfig(t, func(c *config.Config) {
		c.Backends = []string{bad.URL, good.URL}
	})

	w := submit(t, raw)

	if w.Code != http.StatusAccepted || w.Body.String() != hash {
		t.Fatalf("got %d %q, want 202 %q", w.Code, w.Body.String(), hash)
	}
	waitFor(t, badCh)
	waitFor(t, goodCh)
}

// An unreachable backend must not fail or block the request.
func TestSubmitTxUnreachableBackend(t *testing.T) {
	raw, _ := testTx(t)
	withConfig(t, func(c *config.Config) {
		c.Backends = []string{"http://127.0.0.1:1"}
	})
	if w := submit(t, raw); w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want 202", w.Code)
	}
}

// A transaction-shaped array whose body is not a map takes the
// parse-failure path, which answers with the error text.
func TestSubmitTxMalformedCBOR(t *testing.T) {
	b, _ := recordingServer(t, http.StatusAccepted, "")
	withConfig(t, func(c *config.Config) { c.Backends = []string{b.URL} })
	raw, _ := cbor.Marshal([]any{"not a body", map[uint]any{}, true, nil})

	w := submit(t, raw)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", w.Code)
	}
}

// Both Maestro submit modes post the raw transaction to their own endpoint
// with the API key header.
func TestSubmitTxMaestro(t *testing.T) {
	for _, tt := range []struct {
		name, wantPath string
		turbo          bool
	}{
		{"standard", "/txmanager", false},
		{"turbo", "/txmanager/turbosubmit", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, hash := testTx(t)
			srv, ch := recordingServer(t, http.StatusAccepted, hash)
			orig := newMaestroClient
			newMaestroClient = func(key, network string) *maestro.Client {
				c := orig(key, network)
				c.BaseUrl = srv.URL
				return c
			}
			t.Cleanup(func() { newMaestroClient = orig })
			withConfig(t, func(c *config.Config) {
				c.Backends = []string{"http://127.0.0.1:1"}
				c.Maestro = config.MaestroConfig{
					ApiKey: "secret", Network: "preprod", TurboTx: tt.turbo,
				}
			})

			if w := submit(t, raw); w.Code != http.StatusAccepted {
				t.Fatalf("status: got %d, want 202", w.Code)
			}

			// The unreachable backend sends nothing, so the only request
			// reaching the recording server is Maestro's.
			r := waitFor(t, ch)
			if r.path != tt.wantPath {
				t.Errorf("path: got %q, want %q", r.path, tt.wantPath)
			}
			if r.apiKey != "secret" {
				t.Errorf("api-key: got %q", r.apiKey)
			}
			if hex.EncodeToString(r.body) != hex.EncodeToString(raw) {
				t.Error("maestro did not receive the unmodified transaction")
			}
		})
	}
}

// testServerConfig is a config that binds loopback, for the server tests.
func testServerConfig() *config.Config {
	return &config.Config{Api: config.ApiConfig{ListenAddress: "127.0.0.1"}}
}

// newServer must set the header and idle timeouts and bind the configured
// address; an unbounded header read lets a slow client hold a connection open.
func TestNewServerConfig(t *testing.T) {
	cfg := testServerConfig()
	cfg.Api.ListenPort = 8123
	srv := newServer(cfg)
	if srv.Addr != "127.0.0.1:8123" {
		t.Errorf("addr: got %q", srv.Addr)
	}
	if srv.ReadHeaderTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Errorf("timeouts unset: header=%v idle=%v",
			srv.ReadHeaderTimeout, srv.IdleTimeout)
	}
}

// startTestServer serves newServer on a free loopback port, over TLS when a
// certificate and key path are given, and returns its base URL. The server is
// shut down when the test ends.
func startTestServer(t *testing.T, tlsFiles ...string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := newServer(testServerConfig())
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	if len(tlsFiles) == 2 {
		go func() { _ = srv.ServeTLS(ln, tlsFiles[0], tlsFiles[1]) }()
		return "https://" + ln.Addr().String()
	}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String()
}

// TestServerServesHealthcheck runs the real server over plain HTTP and checks
// /healthcheck end to end, covering the listener rather than just the handler.
func TestServerServesHealthcheck(t *testing.T) {
	base := startTestServer(t)
	resp, err := http.Get(base + "/healthcheck") //nolint:noctx // local test server
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != `{"failed":false}` {
		t.Errorf("got %d %q", resp.StatusCode, body)
	}
}

// writeSelfSignedCert writes a throwaway ECDSA certificate valid for 127.0.0.1
// into a temp dir and returns the certificate and key file paths.
func writeSelfSignedCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(
		&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(
		&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

// The same handler must serve over TLS with the configured certificate pair.
func TestServerServesTLS(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t)
	base := startTestServer(t, certPath, keyPath)
	pool := x509.NewCertPool()
	pemBytes, _ := os.ReadFile(certPath)
	pool.AppendCertsFromPEM(pemBytes)
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}

	resp, err := client.Get(base + "/healthcheck") //nolint:noctx // local test server
	if err != nil {
		t.Fatalf("tls request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d", resp.StatusCode)
	}
}
