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
	"encoding/hex"
	"net/http"
	"testing"

	"github.com/blinklabs-io/tx-submit-api-mirror/internal/config"
)

// useBlockfrost enables Blockfrost against baseURL; the node backend is
// unreachable so only Blockfrost's request reaches the recording server.
func useBlockfrost(t *testing.T, baseURL string) {
	t.Helper()
	withConfig(t, func(c *config.Config) {
		c.Backends = []string{"http://127.0.0.1:1"}
		c.Blockfrost = config.BlockfrostConfig{
			ApiKey: "secret", Network: "preprod", BaseUrl: baseURL,
		}
	})
}

// Blockfrost receives the raw CBOR at tx/submit with the project ID header.
func TestSubmitTxBlockfrost(t *testing.T) {
	raw, hash := testTx(t)
	srv, ch := recordingServer(t, http.StatusOK, `"`+hash+`"`)
	useBlockfrost(t, srv.URL+"/api/v0/")

	if w := submit(t, raw); w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want 202", w.Code)
	}
	r := waitFor(t, ch)
	if r.method != http.MethodPost || r.path != "/api/v0/tx/submit" {
		t.Errorf("request: got %s %s", r.method, r.path)
	}
	if r.contentType != "application/cbor" {
		t.Errorf("content-type: got %q", r.contentType)
	}
	if r.projectID != "secret" {
		t.Errorf("project_id: got %q", r.projectID)
	}
	if hex.EncodeToString(r.body) != hex.EncodeToString(raw) {
		t.Error("blockfrost did not receive the unmodified transaction")
	}
}

func TestBlockfrostLogRecords(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		logs := captureLogs(t, "info")
		raw, hash := testTx(t)
		srv, ch := recordingServer(t, http.StatusOK, `"`+hash+`"`)
		useBlockfrost(t, srv.URL)
		submit(t, raw)
		waitFor(t, ch)
		expect(t, logs.find(t, "successfully submitted transaction to Blockfrost"),
			map[string]any{"level": "INFO", "tx_hash": hash})
	})
	t.Run("rejected", func(t *testing.T) {
		logs := captureLogs(t, "info")
		raw, _ := testTx(t)
		srv, ch := recordingServer(t, http.StatusBadRequest, "bad tx")
		useBlockfrost(t, srv.URL)
		submit(t, raw)
		waitFor(t, ch)
		rec := logs.find(t, "failed to send request to Blockfrost")
		expect(t, rec, map[string]any{"level": "ERROR"})
		if _, found := rec["error"]; !found {
			t.Error("failure record lacks error")
		}
	})
	t.Run("unsupported network", func(t *testing.T) {
		logs := captureLogs(t, "info")
		raw, _ := testTx(t)
		withConfig(t, func(c *config.Config) {
			c.Backends = []string{"http://127.0.0.1:1"}
			c.Blockfrost = config.BlockfrostConfig{ApiKey: "k", Network: "nope"}
		})
		submit(t, raw)
		expect(t, logs.find(t, "failed to send request to Blockfrost"),
			map[string]any{"level": "ERROR"})
	})
}

// Without an API key Blockfrost is not contacted.
func TestBlockfrostDisabledWithoutKey(t *testing.T) {
	raw, _ := testTx(t)
	srv, ch := recordingServer(t, http.StatusOK, "")
	withConfig(t, func(c *config.Config) {
		c.Backends = []string{"http://127.0.0.1:1"}
		c.Blockfrost = config.BlockfrostConfig{Network: "preprod", BaseUrl: srv.URL}
	})
	submit(t, raw)
	select {
	case r := <-ch:
		t.Fatalf("unexpected request: %s", r.path)
	default:
	}
}

func TestBlockfrostServer(t *testing.T) {
	got, err := blockfrostServer(config.BlockfrostConfig{Network: "preview"})
	if err != nil || got != "https://cardano-preview.blockfrost.io/api/v0" {
		t.Errorf("got %q, %v", got, err)
	}
	got, _ = blockfrostServer(config.BlockfrostConfig{BaseUrl: "http://x/api/v0/"})
	if got != "http://x/api/v0" {
		t.Errorf("base url override: got %q", got)
	}
	if _, err := blockfrostServer(config.BlockfrostConfig{Network: "x"}); err == nil {
		t.Error("expected error for unknown network")
	}
}
