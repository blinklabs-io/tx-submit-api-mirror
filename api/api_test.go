package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blinklabs-io/tx-submit-api-mirror/config"
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
