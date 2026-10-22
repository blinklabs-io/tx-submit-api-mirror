package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blinklabs-io/tx-submit-api-mirror/internal/config"
	"github.com/blinklabs-io/tx-submit-api-mirror/internal/logging"
	"github.com/fxamacker/cbor/v2"
	maestro "github.com/maestro-org/go-sdk/client"
)

type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write implements io.Writer so the pipe reader can copy log output in.
func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// records returns every JSON log line written so far.
func (l *logCapture) records() []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(l.buf.String(), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil {
			out = append(out, rec)
		}
	}
	return out
}

// rawLine returns the first raw log line containing text.
func (l *logCapture) rawLine(text string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range strings.Split(l.buf.String(), "\n") {
		if strings.Contains(line, text) {
			return line
		}
	}
	return ""
}

// find waits for a record with the given message. Submissions are logged from
// goroutines after the response, so a test cannot read the log synchronously.
func (l *logCapture) find(t *testing.T, msg string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, rec := range l.records() {
			if rec["msg"] == msg {
				return rec
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no log record %q; got %v", msg, l.records())
	return nil
}

// captureLogs points the global logger at a buffer by swapping os.Stdout while
// logging.Setup builds its handler, then restores both. The handlers read the
// global logger on each call, so this needs no production hook.
func captureLogs(t *testing.T, level string) *logCapture {
	t.Helper()
	capture := &logCapture{}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	if err := logging.Setup(&config.LoggingConfig{Level: level}); err != nil {
		os.Stdout = stdout
		t.Fatal(err)
	}
	os.Stdout = stdout
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(capture, r)
		close(done)
	}()
	t.Cleanup(func() {
		_ = logging.Setup(&config.LoggingConfig{Level: "info"})
		_ = w.Close()
		<-done
	})
	return capture
}

// expect checks that rec has every key in want with the given value, and that
// it carries the timestamp and level fields every record must have.
func expect(t *testing.T, rec map[string]any, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("%s: got %v, want %v (record %v)", k, rec[k], v, rec)
		}
	}
	for _, k := range []string{"timestamp", "level"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("record lacks %q: %v", k, rec)
		}
	}
}

// TestSubmitTxLogRecords checks the structured log records of a submission:
// the debug transaction ID and dump, the backend success record, a backend
// rejection (status and response body) and an unreachable backend. It guards
// against renamed fields or changed levels that no response would reveal.
func TestSubmitTxLogRecords(t *testing.T) {
	logs := captureLogs(t, "debug")
	raw, hash := testTx(t)
	ok, okCh := recordingServer(t, http.StatusAccepted, "")
	bad, badCh := recordingServer(t, http.StatusBadRequest, "ledger rejected")
	withConfig(t, func(c *config.Config) {
		c.Backends = []string{ok.URL, bad.URL, "http://127.0.0.1:1"}
	})

	submit(t, raw)
	waitFor(t, okCh)
	waitFor(t, badCh)

	expect(t, logs.find(t, "transaction ID"),
		map[string]any{"level": "DEBUG", "tx_hash": hash})
	expect(t, logs.find(t, "transaction dump"),
		map[string]any{"level": "DEBUG", "tx": hexString(raw)})
	rec := logs.find(t, "successfully submitted transaction to backend")
	expect(t, rec, map[string]any{
		"level": "INFO", "tx_hash": hash, "backend": ok.URL,
	})
	for _, k := range []string{"latency", "connReused"} {
		if _, found := rec[k]; !found {
			t.Errorf("success record lacks %q", k)
		}
	}
	expect(t, logs.find(t, "backend rejected transaction"), map[string]any{
		"level": "ERROR", "backend": bad.URL, "status": float64(400),
		"response": "ledger rejected",
	})
	expect(t, logs.find(t, "failed to send request to backend"), map[string]any{
		"level": "ERROR", "backend": "http://127.0.0.1:1",
	})
}

// TestSubmitTxLogsMetadataMessage checks the CIP-20 metadata debug record. The
// message attribute must not be named "msg", which would duplicate slog's own
// message key in the JSON output.
func TestSubmitTxLogsMetadataMessage(t *testing.T) {
	logs := captureLogs(t, "debug")
	raw, _ := testTxWithMessage(t, "hello", "world")
	b, ch := recordingServer(t, http.StatusAccepted, "")
	withConfig(t, func(c *config.Config) { c.Backends = []string{b.URL} })

	submit(t, raw)
	waitFor(t, ch)

	// The attribute must not be named "msg": that collides with slog's own
	// message key and emits two "msg" keys in one JSON object.
	rec := logs.find(t, "metadata msg")
	expect(t, rec, map[string]any{"level": "DEBUG", "message": "hello\nworld"})
	if n := strings.Count(logs.rawLine("metadata msg"), `"msg":`); n != 1 {
		t.Errorf("record has %d \"msg\" keys, want 1", n)
	}
}

// TestMaestroLogRecords checks the success and failure records for both Maestro
// submit modes (standard and turbo) against a local fake Maestro server.
func TestMaestroLogRecords(t *testing.T) {
	for _, tt := range []struct {
		name, okMsg string
		turbo       bool
	}{
		{"standard", "successfully submitted transaction to Maestro", false},
		{"turbo", "successfully submitted transaction to Maestro TurboTx", true},
	} {
		t.Run(tt.name+" success", func(t *testing.T) {
			logs := captureLogs(t, "info")
			raw, hash := testTx(t)
			srv, ch := recordingServer(t, http.StatusAccepted, hash)
			useMaestro(t, srv.URL, tt.turbo)
			submit(t, raw)
			waitFor(t, ch)
			expect(t, logs.find(t, tt.okMsg),
				map[string]any{"level": "INFO", "tx_hash": hash})
		})
		t.Run(tt.name+" failure", func(t *testing.T) {
			logs := captureLogs(t, "info")
			raw, _ := testTx(t)
			srv, ch := recordingServer(t, http.StatusInternalServerError, "")
			useMaestro(t, srv.URL, tt.turbo)
			submit(t, raw)
			waitFor(t, ch)
			rec := logs.find(t, "failed to send request to Maestro")
			expect(t, rec, map[string]any{"level": "ERROR"})
			if _, found := rec["error"]; !found {
				t.Error("failure record lacks error")
			}
		})
	}
}

// useMaestro enables Maestro in the global config and points the client at
// baseURL, restoring both when the test ends.
func useMaestro(t *testing.T, baseURL string, turbo bool) {
	t.Helper()
	orig := newMaestroClient
	newMaestroClient = func(key, network string) *maestro.Client {
		c := orig(key, network)
		c.BaseUrl = baseURL
		return c
	}
	t.Cleanup(func() { newMaestroClient = orig })
	withConfig(t, func(c *config.Config) {
		c.Backends = []string{"http://127.0.0.1:1"}
		c.Maestro = config.MaestroConfig{ApiKey: "k", Network: "preprod", TurboTx: turbo}
	})
}

// Submissions run after the handler returns, so cancelling the request (the
// client hanging up) must not abort them. The backend holds its answer until
// the request context is cancelled; if the submission shared that context it
// would fail instead of succeeding.
func TestSubmitSurvivesRequestCancellation(t *testing.T) {
	logs := captureLogs(t, "info")
	raw, hash := testTx(t)
	release := make(chan struct{})
	arrived := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			close(arrived)
			<-release
			w.WriteHeader(http.StatusAccepted)
		},
	))
	t.Cleanup(backend.Close)
	withConfig(t, func(c *config.Config) { c.Backends = []string{backend.URL} })

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/submit/tx",
		strings.NewReader(string(raw))).WithContext(ctx)
	w := httptest.NewRecorder()
	newHandler(nilLogger()).ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d", w.Code)
	}
	<-arrived
	cancel()
	time.Sleep(50 * time.Millisecond)
	close(release)

	expect(t, logs.find(t, "successfully submitted transaction to backend"),
		map[string]any{"tx_hash": hash})
}

// runServe returns serve's result, or a sentinel if it is still listening after
// a short wait, so a regression that binds a real port fails instead of hanging.
func runServe(t *testing.T, srv *http.Server, cfg *config.Config) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- serve(srv, cfg) }()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		_ = srv.Shutdown(context.Background())
		return errors.New("serve was still listening")
	}
}

// serve treats a shutdown as a clean exit and otherwise reports the listener
// error; the TLS branch is chosen only when both certificate paths are set.
func TestServeReturnValues(t *testing.T) {
	t.Run("shutdown is not an error", func(t *testing.T) {
		srv := newServer(testServerConfig())
		srv.Addr = "127.0.0.1:0"
		_ = srv.Shutdown(context.Background())
		if err := serve(srv, testServerConfig()); err != nil {
			t.Errorf("got %v, want nil", err)
		}
	})
	t.Run("plain listener error is returned", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		srv := newServer(testServerConfig())
		srv.Addr = ln.Addr().String()
		err = runServe(t, srv, testServerConfig())
		if err == nil || !strings.Contains(err.Error(), "address already in use") {
			t.Errorf("got %v, want address in use", err)
		}
	})
	t.Run("tls branch needs both paths", func(t *testing.T) {
		cfg := testServerConfig()
		cfg.Tls = config.TlsConfig{CertFilePath: "/nonexistent/c.pem", KeyFilePath: "/nonexistent/k.pem"}
		srv := newServer(cfg)
		srv.Addr = "127.0.0.1:0"
		err := runServe(t, srv, cfg)
		if err == nil || !strings.Contains(err.Error(), "no such file") {
			t.Errorf("got %v, want certificate load error", err)
		}
		cfg.Tls.KeyFilePath = ""
		srv = newServer(cfg)
		srv.Addr = "127.0.0.1:0"
		_ = srv.Shutdown(context.Background())
		if err := serve(srv, cfg); err != nil {
			t.Errorf("cert without key must use plain listener, got %v", err)
		}
	})
}

// Start logs which listener it is starting and returns the serve error.
func TestStart(t *testing.T) {
	logs := captureLogs(t, "info")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	cfg := testServerConfig()
	cfg.Api.ListenPort = uint(mustAtoi(t, port))

	if err := Start(cfg); err == nil {
		t.Error("expected an error because the port is taken")
	}
	expect(t, logs.find(t, "starting API listener"),
		map[string]any{"level": "INFO", "address": "127.0.0.1", "port": float64(cfg.Api.ListenPort)})

	cfg.Tls = config.TlsConfig{CertFilePath: "/nonexistent/c.pem", KeyFilePath: "/nonexistent/k.pem"}
	if err := Start(cfg); err == nil {
		t.Error("expected a certificate error")
	}
	logs.find(t, "starting API TLS listener")
}

// A client holding a connection open without finishing its headers must be
// dropped once ReadHeaderTimeout passes, not kept forever.
func TestSlowHeadersAreDropped(t *testing.T) {
	srv := newServer(testServerConfig())
	srv.ReadHeaderTimeout = 200 * time.Millisecond
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("GET /healthcheck HTTP/1.1\r\nHost: x\r\n"))
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err = io.ReadAll(conn)
	if netErr, ok := err.(net.Error); ok && netErr != nil && netErr.Timeout() { //nolint:errorlint // a read deadline error is returned directly, never wrapped
		t.Fatal("connection was still open after ReadHeaderTimeout")
	}
}

// Behind a reverse proxy the log records the peer, never the forwarded value a
// client can set, so a client cannot forge the address in the access log.
func TestAccessLogIgnoresForwardedFor(t *testing.T) {
	logs := &logCapture{}
	app := httptest.NewServer(newHandler(slogTo(logs)))
	t.Cleanup(app.Close)
	target, err := url.Parse(app.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(httputil.NewSingleHostReverseProxy(target))
	t.Cleanup(proxy.Close)

	req, err := http.NewRequest(http.MethodGet, proxy.URL+"/healthcheck", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	rec := logs.find(t, "/healthcheck")
	if rec["ip"] != "127.0.0.1" {
		t.Errorf("ip: got %v, want the proxy peer 127.0.0.1", rec["ip"])
	}
	if strings.Contains(logs.buf.String(), "203.0.113.9") {
		t.Error("forwarded address leaked into the access log")
	}
}

// testTxWithMessage returns a transaction carrying CIP-20 (label 674) metadata
// with the given messages.
func testTxWithMessage(t *testing.T, msg ...string) ([]byte, string) {
	t.Helper()
	addr := append([]byte{0x60}, make([]byte, 28)...)
	body := map[uint]any{
		0: [][]any{{make([]byte, 32), uint32(0)}},
		1: []map[uint]any{{0: addr, 1: uint64(1_000_000)}},
		2: uint64(100_000),
	}
	aux := map[uint]any{674: map[string]any{"msg": msg}}
	raw, err := cbor.Marshal([]any{body, map[uint]any{}, true, aux})
	if err != nil {
		t.Fatal(err)
	}
	return raw, ""
}

// hexString is the lowercase hex form the transaction dump log uses.
func hexString(b []byte) string { return hex.EncodeToString(b) }

// slogTo returns a JSON logger writing to w.
func slogTo(w io.Writer) *slog.Logger { return slog.New(slog.NewJSONHandler(w, nil)) }

// mustAtoi parses a port string, failing the test on error.
func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
