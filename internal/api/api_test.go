// Copyright 2024 Blink Labs Software
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/blinklabs-io/tx-submit-api-mirror/internal/config"
)

func TestHTTPClientTimeout(t *testing.T) {
	client := createHTTPClient(&config.Config{
		Api: config.ApiConfig{
			ClientTimeout: 100,
		},
	})

	// Create a test server that introduces a delay
	testServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(
				200 * time.Millisecond,
			) // Delay longer than client timeout
			w.WriteHeader(http.StatusOK)
		}),
	)
	defer testServer.Close()

	// Make a request to the test server
	resp, err := client.Get(testServer.URL)

	// Verify that the request timed out
	if err == nil {
		t.Errorf("Expected timeout error, but got nil")
	}
	if resp != nil {
		t.Errorf("Expected no response, but got %v", resp)
	}
}

func TestHTTPClientTimeoutPass(t *testing.T) {
	client := createHTTPClient(&config.Config{
		Api: config.ApiConfig{
			ClientTimeout: 300,
		},
	})

	// Create a test server that introduces a delay
	testServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(
				200 * time.Millisecond,
			) // Delay shorter than client timeout
			w.WriteHeader(http.StatusOK)
		}),
	)
	defer testServer.Close()

	// Make a request to the test server
	resp, err := client.Get(testServer.URL)

	// Verify that the request did not time out
	if err != nil {
		t.Errorf("Expected no error, but got %v", err)
	}
	if resp == nil {
		t.Errorf("Expected response, but got nil")
	} else if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status OK, but got %v", resp.StatusCode)
	}
}

// newTestHandler builds the full routed handler, writing its access log to buf,
// so tests exercise the same middleware chain as the server.
func newTestHandler(buf *bytes.Buffer) http.Handler {
	return newHandler(slog.New(slog.NewJSONHandler(buf, nil)))
}

// accessLog wraps recoverPanics, so the access record only carries status 500
// if recovery runs inside it rather than letting the panic unwind past the
// access logger.
func TestRecoverPanicsAndAccessLog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	h := accessLog(logger)(recoverPanics(logger)(panicking))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
	out := buf.String()
	if !strings.Contains(out, "panic recovered") {
		t.Errorf("expected panic log, got %q", out)
	}
	if !strings.Contains(out, `"status":500`) {
		t.Errorf("expected access log with status 500, got %q", out)
	}
}

// A broken pipe is a client disconnect, not a server fault: it must be logged
// as a warning without a stack trace and must not be reported as a panic.
func TestRecoverPanicsBrokenPipe(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := recoverPanics(logger)(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) {
			panic(&net.OpError{
				Op:  "write",
				Err: os.NewSyscallError("write", syscall.EPIPE),
			})
		},
	))

	h.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/pipe", nil),
	)

	out := buf.String()
	if strings.Contains(out, "panic recovered") || strings.Contains(out, "stack") {
		t.Errorf("broken pipe must not be logged as a panic, got %q", out)
	}
	if !strings.Contains(out, "client connection closed") {
		t.Errorf("expected warning, got %q", out)
	}
}

// Bodies carry no trailing newline and content types are fixed, because
// clients of the existing API may depend on them.
func TestRoutesResponses(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(&buf)
	tests := []struct {
		name, method, path, body string
		wantStatus               int
		wantType, wantBody       string
	}{
		{
			name: "healthcheck", method: http.MethodGet, path: "/healthcheck",
			wantStatus: http.StatusOK,
			wantType:   "application/json; charset=utf-8",
			wantBody:   `{"failed":false}`,
		},
		{
			name: "submit without backends", method: http.MethodPost,
			path: "/api/submit/tx", body: "zz",
			wantStatus: http.StatusInternalServerError,
			wantType:   "text/plain; charset=utf-8",
			wantBody:   "no backends configured",
		},
		{
			name: "unknown path", method: http.MethodGet, path: "/nope",
			wantStatus: http.StatusNotFound,
			wantType:   "text/plain",
			wantBody:   "404 page not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(
				tt.method, tt.path, strings.NewReader(tt.body)))
			if w.Code != tt.wantStatus {
				t.Errorf("status: got %d, want %d", w.Code, tt.wantStatus)
			}
			if got := w.Header().Get("Content-Type"); got != tt.wantType {
				t.Errorf("content-type: got %q, want %q", got, tt.wantType)
			}
			if w.Body.String() != tt.wantBody {
				t.Errorf("body: got %q, want %q", w.Body.String(), tt.wantBody)
			}
		})
	}
}

// With backends configured, malformed submissions must be rejected with the
// original status codes and bodies. The invalid-type error is a JSON string.
func TestSubmitTxBadRequests(t *testing.T) {
	config.GetConfig().Backends = []string{"http://127.0.0.1:1"}
	t.Cleanup(func() { config.GetConfig().Backends = nil })
	var buf bytes.Buffer
	h := newTestHandler(&buf)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(
		http.MethodPost, "/api/submit/tx", strings.NewReader("zz")))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("content-type: got %q", got)
	}
	want := `"could not parse transaction to determine type"`
	if w.Body.String() != want {
		t.Errorf("body: got %q, want %q", w.Body.String(), want)
	}
}

// The service has always answered 404, never 405 or a redirect, for anything
// that is not exactly a routed method and path. A wrong method, HEAD on a GET
// route, an unclean path and an unknown path must all stay plain 404s, with the
// same unadorned body and content type.
func TestUnroutedRequestsAre404(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(&buf)
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "/api/submit/tx"},
		{http.MethodPut, "/api/submit/tx"},
		{http.MethodPost, "/healthcheck"},
		{http.MethodDelete, "/healthcheck"},
		{http.MethodHead, "/healthcheck"},
		{http.MethodOptions, "/healthcheck"},
		{http.MethodGet, "//healthcheck"},
		{http.MethodGet, "/HEALTHCHECK"},
		{http.MethodGet, "/healthcheck/x"},
		{http.MethodPost, "/healthcheck/"},
		{http.MethodGet, "/api/submit/tx/"},
		{http.MethodGet, "/"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tt.method, tt.path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s: got %d, want 404", tt.method, tt.path, w.Code)
		}
		if got := w.Header().Get("Content-Type"); got != "text/plain" {
			t.Errorf("%s %s: content-type %q", tt.method, tt.path, got)
		}
		if tt.method != http.MethodHead && w.Body.String() != "404 page not found" {
			t.Errorf("%s %s: body %q", tt.method, tt.path, w.Body.String())
		}
		if w.Header().Get("Allow") != "" {
			t.Errorf("%s %s: unexpected Allow header", tt.method, tt.path)
		}
	}
}

// Trailing-slash redirects are answered before CORS, so they carry no CORS
// header even for a cross-origin request.
func TestRedirectsCarryNoCORSHeaders(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(&buf)
	req := httptest.NewRequest(http.MethodGet, "/healthcheck/", nil)
	req.Header.Set("Origin", "https://app.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("got %d, want 301", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("redirect has allow-origin %q", got)
	}
}

// Cross-origin requests get the allow-all header; a preflight is answered
// with 204 and the method/header allowance; same-origin requests (no Origin)
// are left untouched.
func TestCORS(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(&buf)

	t.Run("simple cross-origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthcheck", nil)
		req.Header.Set("Origin", "https://app.example")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("allow-origin: got %q, want *", got)
		}
		if w.Code != http.StatusOK {
			t.Errorf("status: got %d", w.Code)
		}
	})

	t.Run("preflight", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/api/submit/tx", nil)
		req.Header.Set("Origin", "https://app.example")
		req.Header.Set("Access-Control-Request-Method", "POST")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusNoContent {
			t.Errorf("status: got %d, want 204", w.Code)
		}
		for k, want := range map[string]string{
			"Access-Control-Allow-Origin":  "*",
			"Access-Control-Allow-Methods": corsAllowMethods,
			"Access-Control-Allow-Headers": corsAllowHeaders,
			"Access-Control-Max-Age":       corsMaxAge,
		} {
			if got := w.Header().Get(k); got != want {
				t.Errorf("%s: got %q, want %q", k, got, want)
			}
		}
	})

	t.Run("no origin", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthcheck", nil))
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("unexpected allow-origin %q", got)
		}
	})
}

// TestClientIPIgnoresForwardingHeaders checks that the logged client address
// is the TCP peer and that a client-supplied X-Forwarded-For is not trusted.
func TestClientIPIgnoresForwardingHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.1:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := clientIP(req); got != "192.0.2.1" {
		t.Errorf("got %q, want 192.0.2.1", got)
	}
}

// A handler that writes a body without calling WriteHeader answers 200, and
// the access record must say so rather than report an unset status.
func TestAccessLogImplicitStatus(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := accessLog(logger)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) },
	))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if !strings.Contains(buf.String(), `"status":200`) {
		t.Errorf("expected status 200 in access log, got %q", buf.String())
	}
}

// A trailing slash redirects to the canonical path. POST must use 307, which
// makes the client resend the transaction body; 301 would degrade it to a GET.
func TestTrailingSlashRedirects(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(&buf)
	for _, tt := range []struct {
		method, path, wantLoc string
		wantCode              int
	}{
		{http.MethodGet, "/healthcheck/", "/healthcheck", http.StatusMovedPermanently},
		{http.MethodPost, "/api/submit/tx/", "/api/submit/tx", http.StatusTemporaryRedirect},
		{http.MethodPost, "/api/submit/tx/?a=1", "/api/submit/tx?a=1", http.StatusTemporaryRedirect},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tt.method, tt.path, nil))
		if w.Code != tt.wantCode || w.Header().Get("Location") != tt.wantLoc {
			t.Errorf("%s %s: got %d %q, want %d %q", tt.method, tt.path,
				w.Code, w.Header().Get("Location"), tt.wantCode, tt.wantLoc)
		}
	}
}

// Any OPTIONS request that carries an Origin is a preflight, even without
// Access-Control-Request-Method: it is answered 204 with the allowance headers
// and never reaches routing, which would otherwise return 404.
func TestCORSPreflightWithoutRequestMethod(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(&buf)
	req := httptest.NewRequest(http.MethodOptions, "/healthcheck", nil)
	req.Header.Set("Origin", "http://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want 204", w.Code)
	}
	for k, want := range map[string]string{
		"Access-Control-Allow-Origin":  "*",
		"Access-Control-Allow-Methods": corsAllowMethods,
		"Access-Control-Allow-Headers": corsAllowHeaders,
		"Access-Control-Max-Age":       corsMaxAge,
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s: got %q, want %q", k, got, want)
		}
	}
}

// An Origin equal to the request's own host is a same-origin fetch, not a CORS
// request: no CORS headers are added and an OPTIONS falls through to routing.
func TestCORSSameOriginIsUntouched(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(&buf)
	for _, scheme := range []string{"http", "https"} {
		get := httptest.NewRequest(http.MethodGet, "/healthcheck", nil)
		get.Header.Set("Origin", scheme+"://"+get.Host)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, get)
		if w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s GET: got %d with allow-origin %q", scheme, w.Code,
				w.Header().Get("Access-Control-Allow-Origin"))
		}

		opts := httptest.NewRequest(http.MethodOptions, "/healthcheck", nil)
		opts.Header.Set("Origin", scheme+"://"+opts.Host)
		opts.Header.Set("Access-Control-Request-Method", "GET")
		w = httptest.NewRecorder()
		h.ServeHTTP(w, opts)
		if w.Code != http.StatusNotFound || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s OPTIONS: got %d with allow-origin %q", scheme, w.Code,
				w.Header().Get("Access-Control-Allow-Origin"))
		}
	}
}

// A recovered panic answers a bare 500 with no body, as the service always has.
func TestRecoveredPanicHasEmptyBody(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h := recoverPanics(logger)(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) { panic("boom") },
	))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("body: got %q, want empty", w.Body.String())
	}
}

// A client disconnect panic is recorded on the access log as "errors", the
// attribute the service has always attached to that path.
func TestAccessLogRecordsBrokenPipeError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := accessLog(logger)(recoverPanics(logger)(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) {
			panic(&net.OpError{
				Op:  "write",
				Err: os.NewSyscallError("write", syscall.EPIPE),
			})
		},
	)))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/pipe", nil))
	want := `"errors":"Error #01: write: write: broken pipe\n"`
	if !strings.Contains(buf.String(), want) {
		t.Errorf("access record lacks %s, got %q", want, buf.String())
	}
}

// Only the first status counts. A handler that sends 200 and then panics has
// already told the client 200, so the access record must not say 500.
func TestAccessLogKeepsFirstStatus(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := accessLog(logger)(recoverPanics(logger)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			panic("after headers")
		},
	)))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if !strings.Contains(buf.String(), `"status":200`) {
		t.Errorf("expected status 200, got %q", buf.String())
	}
}

// A proxy that mounts the service under a prefix and strips it sends
// X-Forwarded-Prefix. The redirect must stay under that prefix, otherwise the
// client's repeated submission lands outside the service and gets a 404. The
// expected locations are the ones the service has always returned, including
// the sanitizing of unsafe characters and the odd result for a prefix without a
// leading slash.
func TestTrailingSlashRedirectKeepsForwardedPrefix(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(&buf)
	for _, tt := range []struct {
		method, path, prefix, wantLoc string
		wantCode                      int
	}{
		{http.MethodPost, "/api/submit/tx/", "/mounted", "/mounted/api/submit/tx", 307},
		{http.MethodPost, "/api/submit/tx/", "/mounted/", "/mounted/api/submit/tx", 307},
		{http.MethodPost, "/api/submit/tx/", "/a/b", "/a/b/api/submit/tx", 307},
		{http.MethodPost, "/api/submit/tx/", "//x//y", "/x/y/api/submit/tx", 307},
		{http.MethodPost, "/api/submit/tx/", "/a b", "/ab/api/submit/tx", 307},
		{http.MethodPost, "/api/submit/tx/", "/a?b=1", "/ab1/api/submit/tx", 307},
		{http.MethodPost, "/api/submit/tx/", "/../etc", "/etc/api/submit/tx", 307},
		{http.MethodPost, "/api/submit/tx/", "mounted", "mounted/api/submit/mounted/api/submit/tx", 307},
		{http.MethodPost, "/api/submit/tx/", ".", "/api/submit/tx", 307},
		{http.MethodPost, "/api/submit/tx/", "/", "/api/submit/tx", 307},
		{http.MethodPost, "/api/submit/tx/", "", "/api/submit/tx", 307},
		{http.MethodGet, "/healthcheck/", "/mounted", "/mounted/healthcheck", 301},
		{http.MethodGet, "/healthcheck/?q=1", "/mounted", "/mounted/healthcheck?q=1", 301},
	} {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		if tt.prefix != "" {
			req.Header.Set("X-Forwarded-Prefix", tt.prefix)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tt.wantCode || w.Header().Get("Location") != tt.wantLoc {
			t.Errorf("%s %s prefix %q: got %d %q, want %d %q", tt.method, tt.path,
				tt.prefix, w.Code, w.Header().Get("Location"), tt.wantCode, tt.wantLoc)
		}
	}
}
