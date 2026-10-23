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
