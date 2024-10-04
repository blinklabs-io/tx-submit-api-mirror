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
	"net/http"
	"net/http/httptest"
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
