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
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptrace"
	"runtime/debug"
	"strings"
	"time"

	models "github.com/blinklabs-io/cardano-models"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/tx-submit-api-mirror/internal/config"
	"github.com/blinklabs-io/tx-submit-api-mirror/internal/logging"
	"github.com/fxamacker/cbor/v2"
	cors "github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	maestro "github.com/maestro-org/go-sdk/client"
)

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
	// Disable gin debug output
	gin.SetMode(gin.ReleaseMode)
	gin.DisableConsoleColor()

	// Configure router
	router := gin.New()
	// Configure cors
	router.Use(cors.Default())
	// Access logging, then panic recovery so recovered requests are still logged
	accessLogger := logging.GetAccessLogger()
	router.Use(accessLog(accessLogger))
	router.Use(recoverPanics(accessLogger))

	// Configure routes
	router.GET("/healthcheck", handleHealthcheck)
	router.POST("/api/submit/tx", handleSubmitTx)

	// Start API listener
	if cfg.Tls.CertFilePath != "" && cfg.Tls.KeyFilePath != "" {
		return router.RunTLS(
			fmt.Sprintf("%s:%d", cfg.Api.ListenAddress, cfg.Api.ListenPort),
			cfg.Tls.CertFilePath,
			cfg.Tls.KeyFilePath,
		)
	} else {
		return router.Run(fmt.Sprintf("%s:%d",
			cfg.Api.ListenAddress,
			cfg.Api.ListenPort))
	}
}

// accessLog logs one record per request after the handler chain completes.
func accessLog(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery
		c.Next()
		attrs := []any{
			"status", c.Writer.Status(),
			"method", c.Request.Method,
			"path", path,
			"query", query,
			"ip", c.ClientIP(),
			"user-agent", c.Request.UserAgent(),
			"latency", time.Since(start).Seconds(),
		}
		if len(c.Errors) > 0 {
			attrs = append(attrs, "errors", c.Errors.String())
		}
		logger.Info(path, attrs...)
	}
}

// recoverPanics logs a panic and answers 500. http.ErrAbortHandler is
// re-raised because net/http uses it to abort a response deliberately.
func recoverPanics(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler { //nolint:errorlint // sentinel panic value, not an error chain
					panic(rec)
				}
				logger.Error(
					"panic recovered",
					"error", fmt.Sprint(rec),
					"method", c.Request.Method,
					"path", c.Request.URL.Path,
					"stack", string(debug.Stack()),
				)
				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()
		c.Next()
	}
}

func handleHealthcheck(c *gin.Context) {
	// TODO: add some actual health checking here
	c.JSON(200, gin.H{"failed": false})
}

func handleSubmitTx(c *gin.Context) {
	cfg := config.GetConfig()
	logger := logging.GetLogger()
	if len(cfg.Backends) == 0 {
		logger.Error("no backends configured")
		c.String(500, "no backends configured")
		return
	}
	// Read transaction from request body
	rawTx, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.Error("failed to read request body", "error", err)
		c.String(500, "failed to request body")
		return
	}
	if c.Request.Body != nil {
		if err := c.Request.Body.Close(); err != nil {
			logger.Error("failed to close request body", "error", err)
		}
	}
	logger.Debug("transaction dump", "tx", hex.EncodeToString(rawTx))
	// Determine transaction type (era)
	txType, err := ledger.DetermineTransactionType(rawTx)
	if err != nil {
		logger.Error("could not parse transaction to determine type", "error", err)
		c.JSON(400, "could not parse transaction to determine type")
		return
	}
	tx, err := ledger.NewTransactionFromCbor(txType, rawTx)
	if err != nil {
		logger.Error("failed to parse transaction CBOR", "error", err)
		c.String(400, fmt.Sprintf("failed to parse transaction CBOR: %s", err))
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
					"msg", strings.Join(msgMetadata.Num674.Msg, "\n"),
				)
			}
		}
	}
	// Create custom HTTP client
	client := createHTTPClient(cfg)

	// Send request to each backend
	for _, backend := range cfg.Backends {
		go func(backend string) {
			startTime := time.Now()
			body := bytes.NewBuffer(rawTx)
			connReused := false
			// Trace HTTP request to get information about whether the connection was reused
			clientTrace := &httptrace.ClientTrace{
				GotConn: func(info httptrace.GotConnInfo) { connReused = info.Reused },
			}
			traceCtx := httptrace.WithClientTrace(
				context.Background(),
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
		}(backend)
	}
	// Optional Maestro
	if cfg.Maestro.ApiKey != "" {
		go func(cfg *config.Config, rawTx []byte) {
			txHex := hex.EncodeToString(rawTx)
			maestroClient := maestro.NewClient(
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
	c.String(202, tx.Hash().String())
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
