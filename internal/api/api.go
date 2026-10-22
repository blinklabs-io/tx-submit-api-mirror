// Copyright 2025 Blink Labs Software
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
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"runtime/debug"
	"strings"
	"time"

	models "github.com/blinklabs-io/cardano-models"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/tx-submit-api-mirror/internal/config"
	"github.com/blinklabs-io/tx-submit-api-mirror/internal/logging"
	"github.com/fxamacker/cbor/v2"
	maestro "github.com/maestro-org/go-sdk/client"
)

// newMaestroClient is a variable so tests can point the client at a local server.
var newMaestroClient = maestro.NewClient

// newServer builds the API server for cfg. ReadHeaderTimeout bounds slow
// header delivery, which a bare http.ListenAndServe leaves unlimited.
func newServer(cfg *config.Config) *http.Server {
	return &http.Server{
		Addr: fmt.Sprintf(
			"%s:%d",
			cfg.Api.ListenAddress,
			cfg.Api.ListenPort,
		),
		Handler:           newHandler(logging.GetAccessLogger()),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

func Start(cfg *config.Config) error {
	// Standard logging
	logger := logging.GetLogger()
	if cfg.Tls.CertFilePath != "" && cfg.Tls.KeyFilePath != "" {
		logger.Info(
			"starting API TLS listener",
			"address", cfg.Api.ListenAddress,
			"port", cfg.Api.ListenPort,
		)
	} else {
		logger.Info(
			"starting API listener",
			"address", cfg.Api.ListenAddress,
			"port", cfg.Api.ListenPort,
		)
	}
	return serve(newServer(cfg), cfg)
}

// serve runs server until it fails or is shut down. A shutdown is not an
// error, so it returns nil for http.ErrServerClosed.
func serve(server *http.Server, cfg *config.Config) error {
	var err error
	if cfg.Tls.CertFilePath != "" && cfg.Tls.KeyFilePath != "" {
		err = server.ListenAndServeTLS(
			cfg.Tls.CertFilePath,
			cfg.Tls.KeyFilePath,
		)
	} else {
		err = server.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// newHandler builds the routed handler with its middleware. CORS is outermost
// so preflights are answered without being logged as handler traffic, and
// recovery is inside the access log so a recovered panic is still logged with
// its 500 status. Trailing-slash redirects are answered before any of that, as
// they always were, so they carry no CORS headers and no access record.
func newHandler(accessLogger *slog.Logger) http.Handler {
	app := corsMiddleware(
		accessLog(accessLogger)(recoverPanics(accessLogger)(http.HandlerFunc(route))),
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/healthcheck/" && r.Method == http.MethodGet:
			redirectToPath("/healthcheck")(w, r)
		case r.URL.Path == "/api/submit/tx/" && r.Method == http.MethodPost:
			redirectToPath("/api/submit/tx")(w, r)
		default:
			app.ServeHTTP(w, r)
		}
	})
}

// route dispatches on the exact request path and method. It deliberately does
// not use http.ServeMux, which would answer 405 for a wrong method, answer 200
// to HEAD for a GET route, and redirect unclean paths such as "//healthcheck";
// every one of those is a plain 404 here.
func route(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthcheck" && r.Method == http.MethodGet:
		handleHealthcheck(w, r)
	case r.URL.Path == "/api/submit/tx" && r.Method == http.MethodPost:
		handleSubmitTx(w, r)
	default:
		notFound(w)
	}
}

// notFound writes the 404 body and content type clients have always received:
// no charset and no trailing newline, unlike http.NotFound.
func notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, "404 page not found")
}

// redirectToPath sends the request to path, keeping the query string. A POST
// uses 307 so the client repeats the method and body; 301 would turn it into
// a GET and drop the transaction.
func redirectToPath(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target := path
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		code := http.StatusTemporaryRedirect
		if r.Method == http.MethodGet {
			code = http.StatusMovedPermanently
		}
		http.Redirect(w, r, target, code) //nolint:gosec // G710: target is a fixed path plus the request's own query
	}
}

const (
	corsAllowMethods = "GET,POST,PUT,PATCH,DELETE,HEAD,OPTIONS"
	corsAllowHeaders = "Origin,Content-Length,Content-Type"
	corsMaxAge       = "43200"
)

// corsMiddleware allows every origin. Requests without an Origin header are not
// cross-origin and pass through untouched. A preflight is an OPTIONS request
// that also names the method it wants; a bare OPTIONS falls through to the mux.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == "" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions &&
			r.Header.Get("Access-Control-Request-Method") != "" {
			w.Header().Set("Access-Control-Allow-Methods", corsAllowMethods)
			w.Header().Set("Access-Control-Allow-Headers", corsAllowHeaders)
			w.Header().Set("Access-Control-Max-Age", corsMaxAge)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the status code, which net/http does not expose
// after the handler has written it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(status int) {
	sr.status = status
	sr.ResponseWriter.WriteHeader(status)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (sr *statusRecorder) Unwrap() http.ResponseWriter {
	return sr.ResponseWriter
}

// clientIP returns the peer address. Forwarding headers are not consulted
// because they are client-controlled unless a trusted proxy sets them.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// accessLog logs one record per request after the handler chain completes.
func accessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			logger.Info(
				r.URL.Path,
				"status", rec.status,
				"method", r.Method,
				"path", r.URL.Path,
				"query", r.URL.RawQuery,
				"ip", clientIP(r),
				"user-agent", r.UserAgent(),
				"latency", time.Since(start).Seconds(),
			)
		})
	}
}

// recoverPanics logs a panic and answers 500. http.ErrAbortHandler is
// re-raised because net/http uses it to abort a response deliberately. A
// broken pipe means the client went away, so it is logged without a stack and
// no status is written to the dead connection.
func recoverPanics(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if rec == http.ErrAbortHandler { //nolint:errorlint // sentinel panic value, not an error chain
						panic(rec)
					}
					if isBrokenPipe(rec) {
						logger.Warn(
							"client connection closed",
							"error", fmt.Sprint(rec),
							"method", r.Method,
							"path", r.URL.Path,
						)
						return
					}
					logger.Error(
						"panic recovered",
						"error", fmt.Sprint(rec),
						"method", r.Method,
						"path", r.URL.Path,
						"stack", string(debug.Stack()),
					)
					http.Error(
						w,
						http.StatusText(http.StatusInternalServerError),
						http.StatusInternalServerError,
					)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func isBrokenPipe(rec any) bool {
	err, ok := rec.(error)
	if !ok {
		return false
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		return false
	}
	var sysErr *os.SyscallError
	if !errors.As(opErr.Err, &sysErr) {
		return false
	}
	msg := strings.ToLower(sysErr.Error())
	return strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset by peer")
}

// writeText and writeJSON write the body without a trailing newline, which
// clients of the existing API may compare byte for byte.
func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		logging.GetLogger().Error("failed to encode JSON response", "error", err)
		writeText(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func handleHealthcheck(w http.ResponseWriter, _ *http.Request) {
	// TODO: add some actual health checking here
	writeJSON(w, http.StatusOK, map[string]bool{"failed": false})
}

func handleSubmitTx(w http.ResponseWriter, r *http.Request) {
	cfg := config.GetConfig()
	logger := logging.GetLogger()
	if len(cfg.Backends) == 0 {
		logger.Error("no backends configured")
		writeText(w, http.StatusInternalServerError, "no backends configured")
		return
	}
	// Read transaction from request body
	rawTx, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Error("failed to read request body", "error", err)
		writeText(w, http.StatusInternalServerError, "failed to request body")
		return
	}
	if r.Body != nil {
		if err := r.Body.Close(); err != nil {
			logger.Error("failed to close request body", "error", err)
		}
	}
	logger.Debug("transaction dump", "tx", hex.EncodeToString(rawTx))
	// Determine transaction type (era)
	txType, err := ledger.DetermineTransactionType(rawTx)
	if err != nil {
		logger.Error("could not parse transaction to determine type", "error", err)
		writeJSON(w, http.StatusBadRequest, "could not parse transaction to determine type")
		return
	}
	tx, err := ledger.NewTransactionFromCbor(txType, rawTx)
	if err != nil {
		logger.Error("failed to parse transaction CBOR", "error", err)
		writeText(w, http.StatusBadRequest, fmt.Sprintf("failed to parse transaction CBOR: %s", err))
		return
	}
	logger.Debug("transaction ID", "tx_hash", tx.Hash().String())
	// Debug log metadata messages
	if tx.Metadata() != nil {
		mdCbor := tx.Metadata().Cbor()
		var msgMetadata models.Cip20Metadata
		err := cbor.Unmarshal(mdCbor, &msgMetadata)
		if err == nil {
			if msgMetadata.Num674.Msg != nil {
				logger.Debug(
					"metadata msg",
					"message", strings.Join(msgMetadata.Num674.Msg, "\n"),
				)
			}
		}
	}
	// Create custom HTTP client
	client := createHTTPClient(cfg)

	// Submissions outlive the request, so they must not be cancelled when
	// the handler returns.
	submitCtx := context.WithoutCancel(r.Context())
	// Send request to each backend
	for _, backend := range cfg.Backends {
		go func(ctx context.Context, backend string) {
			startTime := time.Now()
			body := bytes.NewBuffer(rawTx)
			connReused := false
			// Trace HTTP request to get information about whether the connection was reused
			clientTrace := &httptrace.ClientTrace{
				GotConn: func(info httptrace.GotConnInfo) { connReused = info.Reused },
			}
			traceCtx := httptrace.WithClientTrace(
				ctx,
				clientTrace,
			)
			req, err := http.NewRequestWithContext(
				traceCtx,
				http.MethodPost,
				backend,
				body,
			)
			if err != nil {
				logger.Error("failed to create request", "backend", backend, "error", err)
				return
			}
			req.Header.Add("Content-Type", "application/cbor")
			resp, err := client.Do(req) //nolint:gosec // G704: backend URL is from admin-controlled config
			if err != nil {
				logger.Error(
					"failed to send request to backend",
					"backend", backend,
					"error", err,
				)
				return
			}
			elapsedTime := time.Since(startTime)
			// We have to read the entire response body and close it to prevent a memory leak
			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				logger.Error("failed to read response body", "backend", backend, "error", err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusAccepted {
				logger.Info(
					"successfully submitted transaction to backend",
					"tx_hash", tx.Hash().String(),
					"backend", backend,
					"latency", elapsedTime.Seconds(),
					"connReused", connReused,
				)
			} else {
				logger.Error(
					"backend rejected transaction",
					"backend", backend,
					"status", resp.StatusCode,
					"response", string(respBody),
					"latency", elapsedTime.Seconds(),
					"connReused", connReused,
				)
			}
		}(submitCtx, backend)
	}
	// Optional Maestro
	if cfg.Maestro.ApiKey != "" {
		go func(cfg *config.Config, rawTx []byte) {
			txHex := hex.EncodeToString(rawTx)
			maestroClient := newMaestroClient(
				cfg.Maestro.ApiKey,
				cfg.Maestro.Network,
			)
			startTime := time.Now()
			if cfg.Maestro.TurboTx {
				_, err := maestroClient.TxManagerSubmitTurbo(txHex)
				elapsedTime := time.Since(startTime)
				if err != nil {
					logger.Error(
						"failed to send request to Maestro",
						"error", err,
						"latency", elapsedTime.Seconds(),
					)
					return
				}
				logger.Info(
					"successfully submitted transaction to Maestro TurboTx",
					"tx_hash", tx.Hash().String(),
					"latency", elapsedTime.Seconds(),
				)
				return
			}
			_, err := maestroClient.TxManagerSubmit(txHex)
			elapsedTime := time.Since(startTime)
			if err != nil {
				logger.Error(
					"failed to send request to Maestro",
					"error", err,
					"latency", elapsedTime.Seconds(),
				)
				return
			}
			logger.Info(
				"successfully submitted transaction to Maestro",
				"tx_hash", tx.Hash().String(),
				"latency", elapsedTime.Seconds(),
			)
		}(cfg, rawTx)
	}
	// Return transaction ID
	writeText(w, http.StatusAccepted, tx.Hash().String())
}

func createHTTPClient(cfg *config.Config) *http.Client {
	timeout := int64(60000)
	if cfg.Api.ClientTimeout < math.MaxInt64 {
		timeout = int64(cfg.Api.ClientTimeout) // #nosec G115
	}
	return &http.Client{
		Timeout: time.Duration(timeout) * time.Millisecond,
		Transport: &http.Transport{
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			DisableCompression:    false,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}
