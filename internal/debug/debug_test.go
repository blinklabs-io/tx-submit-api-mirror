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

package debug

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blinklabs-io/tx-submit-api-mirror/internal/config"
)

func testConfig(address string, port uint) *config.Config {
	return &config.Config{
		Debug: config.DebugConfig{ListenAddress: address, ListenPort: port},
	}
}

// The server binds the configured address and port, and bounds how long a
// client may take to send headers.
func TestNewServerAddress(t *testing.T) {
	srv := NewServer(testConfig("127.0.0.1", 6060))
	if srv.Addr != "127.0.0.1:6060" {
		t.Errorf("addr: got %q, want 127.0.0.1:6060", srv.Addr)
	}
	if srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout is unset")
	}
}

// The listen address must be valid for IPv4, hostnames, IPv6 and the wildcard
// forms. An IPv6 address needs brackets before the port; without them
// ListenAndServe fails with "too many colons" and the service exits.
func TestNewServerAddressForms(t *testing.T) {
	for _, tt := range []struct {
		address string
		port    uint
		want    string
	}{
		{"localhost", 6060, "localhost:6060"},
		{"127.0.0.1", 6060, "127.0.0.1:6060"},
		{"0.0.0.0", 6060, "0.0.0.0:6060"},
		{"", 6060, ":6060"},
		{"::1", 6060, "[::1]:6060"},
		{"::", 6060, "[::]:6060"},
		{"2001:db8::1", 6060, "[2001:db8::1]:6060"},
	} {
		got := NewServer(testConfig(tt.address, tt.port)).Addr
		if got != tt.want {
			t.Errorf("%q: got %q, want %q", tt.address, got, tt.want)
		}
	}
}

// The address NewServer builds for IPv6 loopback must actually bind.
func TestNewServerBindsIPv6Loopback(t *testing.T) {
	probe, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	_ = probe.Close()

	srv := NewServer(testConfig("::1", 0))
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		t.Fatalf("listen on %q: %v", srv.Addr, err)
	}
	defer ln.Close()
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	resp, err := http.Get("http://" + ln.Addr().String() + "/debug/pprof/") //nolint:noctx // local test server
	if err != nil {
		t.Fatalf("request over IPv6: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
}

// Every pprof endpoint a profiler needs is served, and a path outside
// /debug/pprof/ is not.
func TestNewServerServesPprof(t *testing.T) {
	h := NewServer(testConfig("127.0.0.1", 6060)).Handler
	for _, tt := range []struct {
		path     string
		wantCode int
		wantBody string
	}{
		{"/debug/pprof/", http.StatusOK, "goroutine"},
		{"/debug/pprof/cmdline", http.StatusOK, ""},
		{"/debug/pprof/symbol", http.StatusOK, "num_symbols"},
		{"/debug/pprof/goroutine?debug=1", http.StatusOK, "goroutine profile"},
		{"/debug/pprof/heap?debug=1", http.StatusOK, "heap profile"},
		{"/healthcheck", http.StatusNotFound, ""},
		{"/", http.StatusNotFound, ""},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if w.Code != tt.wantCode {
			t.Errorf("%s: got %d, want %d", tt.path, w.Code, tt.wantCode)
		}
		body, _ := io.ReadAll(w.Body)
		if !strings.Contains(string(body), tt.wantBody) {
			t.Errorf("%s: body lacks %q", tt.path, tt.wantBody)
		}
	}
}
