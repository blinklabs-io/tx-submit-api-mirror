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
	"math"
	"net/http"
	"net/http/httptrace"
	"strings"
	"time"

	models "github.com/blinklabs-io/cardano-models"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/tx-submit-api-mirror/internal/config"
	"github.com/blinklabs-io/tx-submit-api-mirror/internal/logging"
	"github.com/fxamacker/cbor/v2"
	cors "github.com/gin-contrib/cors"
	ginzap "github.com/gin-contrib/zap"
	"github.com/gin-gonic/gin"
	maestro "github.com/maestro-org/go-sdk/client"
)

func Start(cfg *config.Config) error {
	// Standard logging
	logger := logging.GetLogger()
	if cfg.Tls.CertFilePath != "" && cfg.Tls.KeyFilePath != "" {
		logger.Infof(
			"starting API TLS listener on %s:%d",
			cfg.Api.ListenAddress,
			cfg.Api.ListenPort,
		)
	} else {
		logger.Infof(
			"starting API listener on %s:%d",
			cfg.Api.ListenAddress,
			cfg.Api.ListenPort,
		)
	}
	// Disable gin debug output
	gin.SetMode(gin.ReleaseMode)
	gin.DisableConsoleColor()

	// Configure router
	router := gin.New()
	// Catch panics and return a 500
	router.Use(gin.Recovery())
	// Configure cors
	router.Use(cors.Default())
	// Access logging
	accessLogger := logging.GetAccessLogger()
	router.Use(ginzap.Ginzap(accessLogger, "", true))
	router.Use(ginzap.RecoveryWithZap(accessLogger, true))

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

func handleHealthcheck(c *gin.Context) {
	// TODO: add some actual health checking here
	c.JSON(200, gin.H{"failed": false})
}

func handleSubmitTx(c *gin.Context) {
	cfg := config.GetConfig()
	logger := logging.GetLogger()
	if len(cfg.Backends) == 0 {
		logger.Errorf("no backends configured")
		c.String(500, "no backends configured")
		return
	}
	// Read transaction from request body
	rawTx, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.Errorf("failed to read request body: %s", err)
		c.String(500, "failed to request body")
		return
	}
	if c.Request.Body != nil {
		if err := c.Request.Body.Close(); err != nil {
			logger.Errorf("failed to close request body: %s", err)
		}
	}
	logger.Debugf("transaction dump: %x", rawTx)
	// Determine transaction type (era)
	txType, err := ledger.DetermineTransactionType(rawTx)
	if err != nil {
		logger.Errorf("could not parse transaction to determine type: %s", err)
		c.JSON(400, "could not parse transaction to determine type")
		return
	}
	tx, err := ledger.NewTransactionFromCbor(txType, rawTx)
	if err != nil {
		logger.Errorf("failed to parse transaction CBOR: %s", err)
		c.String(400, fmt.Sprintf("failed to parse transaction CBOR: %s", err))
		return
	}
	logger.Debugf("transaction ID: %s", tx.Hash())
	// Debug log metadata messages
	if tx.Metadata() != nil {
		mdCbor := tx.Metadata().Cbor()
		var msgMetadata models.Cip20Metadata
		err := cbor.Unmarshal(mdCbor, &msgMetadata)
		if err == nil {
			if msgMetadata.Num674.Msg != nil {
				logger.Debugf(
					"metadata msg: %s",
					strings.Join(msgMetadata.Num674.Msg, "\n"),
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
				logger.Errorf("failed to create request: %s", err)
				return
			}
			req.Header.Add("Content-Type", "application/cbor")
			resp, err := client.Do(req)
			if err != nil {
				logger.Errorf(
					"failed to send request to backend %s: %s",
					backend,
					err,
				)
				return
			}
			elapsedTime := time.Since(startTime)
			// We have to read the entire response body and close it to prevent a memory leak
			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				logger.Errorf("failed to read response body: %s", err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusAccepted {
				logger.Infow(
					fmt.Sprintf(
						"successfully submitted transaction %s to backend %s",
						tx.Hash(),
						backend,
					),
					"latency",
					elapsedTime.Seconds(),
					"connReused",
					connReused,
				)
			} else {
				logger.Errorw(
					fmt.Sprintf(
						"failed to send request to backend %s: got response %d, %s",
						backend,
						resp.StatusCode,
						string(respBody),
					),
					"latency",
					elapsedTime.Seconds(),
					"connReused",
					connReused,
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
					logger.Errorw(
						fmt.Sprintf(
							"failed to send request to Maestro: got response %v",
							err,
						),
						"latency",
						elapsedTime.Seconds(),
					)
					return
				}
				logger.Infow(
					fmt.Sprintf(
						"successfully submitted transaction %s to Maestro TurboTx",
						tx.Hash(),
					),
					"latency",
					elapsedTime.Seconds(),
				)
				return
			}
			_, err := maestroClient.TxManagerSubmit(txHex)
			elapsedTime := time.Since(startTime)
			if err != nil {
				logger.Errorw(
					fmt.Sprintf(
						"failed to send request to Maestro: got response %v",
						err,
					),
					"latency",
					elapsedTime.Seconds(),
				)
				return
			}
			logger.Infow(
				fmt.Sprintf(
					"successfully submitted transaction %s to Maestro",
					tx.Hash(),
				),
				"latency",
				elapsedTime.Seconds(),
			)
		}(cfg, rawTx)
	}
	// Return transaction ID
	c.String(202, tx.Hash().String())
}

// createHTTPClient with custom timeout
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
