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
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/blinklabs-io/tx-submit-api-mirror/internal/config"
	blockfrost "github.com/blockfrost/blockfrost-go"
)

var blockfrostServers = map[string]string{
	"mainnet": blockfrost.CardanoMainNet,
	"preprod": blockfrost.CardanoPreProd,
	"preview": blockfrost.CardanoPreview,
}

// blockfrostServer returns the API URL, preferring an explicit BaseUrl. The SDK
// appends "/tx/submit" with a slash, so a trailing slash would double it.
func blockfrostServer(cfg config.BlockfrostConfig) (string, error) {
	if cfg.BaseUrl != "" {
		return strings.TrimRight(cfg.BaseUrl, "/"), nil
	}
	server, ok := blockfrostServers[cfg.Network]
	if !ok {
		return "", fmt.Errorf("unsupported Blockfrost network %q", cfg.Network)
	}
	return server, nil
}

// submitBlockfrost submits rawTx through the Blockfrost SDK. The caller's HTTP
// client is passed in so the configured timeout applies and the SDK's default
// retrying client is not used.
func submitBlockfrost(
	ctx context.Context,
	client *http.Client,
	cfg config.BlockfrostConfig,
	rawTx []byte,
	txHash string,
	logger *slog.Logger,
) {
	server, err := blockfrostServer(cfg)
	if err != nil {
		logger.Error("failed to send request to Blockfrost", "error", err)
		return
	}
	bf := blockfrost.NewAPIClient(blockfrost.APIClientOptions{
		ProjectID: cfg.ApiKey,
		Server:    server,
		Client:    client,
	})
	startTime := time.Now()
	_, err = bf.TransactionSubmit(ctx, rawTx)
	elapsedTime := time.Since(startTime)
	if err != nil {
		logger.Error(
			"failed to send request to Blockfrost",
			"error", err,
			"latency", elapsedTime.Seconds(),
		)
		return
	}
	logger.Info(
		"successfully submitted transaction to Blockfrost",
		"tx_hash", txHash,
		"latency", elapsedTime.Seconds(),
	)
}
