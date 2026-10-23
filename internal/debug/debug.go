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
	"fmt"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/blinklabs-io/tx-submit-api-mirror/internal/config"
)

// NewServer returns the debug server, which serves the pprof endpoints on the
// configured address. Importing net/http/pprof also registers its handlers on
// http.DefaultServeMux, but neither listener serves that mux: the API has its
// own router, and this server routes through its own, so the endpoints are
// reachable only on the debug address.
func NewServer(cfg *config.Config) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return &http.Server{
		Addr: fmt.Sprintf(
			"%s:%d",
			cfg.Debug.ListenAddress,
			cfg.Debug.ListenPort,
		),
		Handler:           mux,
		ReadHeaderTimeout: 60 * time.Second,
	}
}
