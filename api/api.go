package api

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"github.com/cloudstruct/tx-submit-api-mirror/config"
	"github.com/cloudstruct/tx-submit-api-mirror/logging"
	"github.com/fxamacker/cbor/v2"
	ginzap "github.com/gin-contrib/zap"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/blake2b"
	"io/ioutil"
	"net/http"
)

func Start(cfg *config.Config) error {
	// Disable gin debug output
	gin.SetMode(gin.ReleaseMode)
	gin.DisableConsoleColor()

	// Configure router
	router := gin.New()
	// Catch panics and return a 500
	router.Use(gin.Recovery())
	// Access logging
	accessLogger := logging.GetAccessLogger()
	router.Use(ginzap.Ginzap(accessLogger, "", true))
	router.Use(ginzap.RecoveryWithZap(accessLogger, true))

	// Configure routes
	router.GET("/healthcheck", handleHealthcheck)
	router.POST("/api/submit/tx", handleSubmitTx)

	// Start listener
	err := router.Run(fmt.Sprintf("%s:%d", cfg.Api.ListenAddress, cfg.Api.ListenPort))
	return err
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
	rawTx, err := ioutil.ReadAll(c.Request.Body)
	if err != nil {
		logger.Errorf("failed to read request body: %s", err)
		c.String(500, "failed to request body")
		return
	}
	if err := c.Request.Body.Close(); err != nil {
		logger.Errorf("failed to close request body: %s", err)
	}
	logger.Debugf("transaction dump: %x", rawTx)
	// Unwrap transaction and calculate ID
	var txUnwrap []cbor.RawMessage
	if err := cbor.Unmarshal(rawTx, &txUnwrap); err != nil {
		logger.Errorf("failed to unwrap transaction CBOR: %s", err)
		c.String(400, fmt.Sprintf("failed to unwrap transaction CBOR: %s", err))
		return
	}
	txId := blake2b.Sum256(txUnwrap[0])
	txIdHex := hex.EncodeToString(txId[:])
	logger.Debugf("calculated transaction ID %s", txIdHex)
	// Send request to each backend
	for _, backend := range cfg.Backends {
		go func(backend string) {
			body := bytes.NewBuffer(rawTx)
			resp, err := http.Post(backend, "application/cbor", body)
			if err != nil {
				logger.Errorf("failed to send request to backend %s: %s", backend, err)
				return
			}
			if resp.StatusCode == 202 {
				logger.Infof("successfully submitted transaction %s to backend %s", txIdHex, backend)
			} else {
				respBody, err := ioutil.ReadAll(resp.Body)
				if err != nil {
					logger.Errorf("failed to read response body: %s", err)
					return
				}
				logger.Errorf("failed to send request to backend %s: got response %d, %s", backend, resp.StatusCode, string(respBody))
			}
		}(backend)
	}
	// Return transaction ID
	c.String(202, txIdHex)
}
