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

package config

import (
	"os"
	"testing"
)

// withCleanConfig runs a test against a fresh copy of the global config and
// restores it afterwards, since Load mutates the global.
func withCleanConfig(t *testing.T) {
	t.Helper()
	saved := *globalConfig
	t.Cleanup(func() { *globalConfig = saved })
}

// unsetenv removes a variable for the test and restores it afterwards. An
// empty value is not the same as unset: envconfig fails to parse "" as a port.
func unsetenv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

// The debug listener is off and bound to loopback by default, so enabling it
// by setting only a port does not expose pprof to the network.
func TestDebugDefaults(t *testing.T) {
	withCleanConfig(t)
	unsetenv(t, "DEBUG_ADDRESS")
	unsetenv(t, "DEBUG_PORT")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Debug.ListenAddress != "localhost" {
		t.Errorf("address: got %q, want localhost", cfg.Debug.ListenAddress)
	}
	if cfg.Debug.ListenPort != 0 {
		t.Errorf("port: got %d, want 0 (disabled)", cfg.Debug.ListenPort)
	}
}

// DEBUG_ADDRESS and DEBUG_PORT override the defaults.
func TestDebugFromEnvironment(t *testing.T) {
	withCleanConfig(t)
	t.Setenv("DEBUG_ADDRESS", "0.0.0.0")
	t.Setenv("DEBUG_PORT", "6060")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Debug.ListenAddress != "0.0.0.0" || cfg.Debug.ListenPort != 6060 {
		t.Errorf("got %s:%d, want 0.0.0.0:6060",
			cfg.Debug.ListenAddress, cfg.Debug.ListenPort)
	}
}
