// Copyright 2024 Blink Labs Software
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package logging

import (
	"log/slog"
	"testing"

	"github.com/blinklabs-io/tx-submit-api-mirror/internal/config"
)

// An empty level must fall back to info rather than fail, since LOGGING_LEVEL
// is optional, while an unrecognized level must surface as an error instead of
// silently logging at the wrong verbosity.
func TestSetupLevel(t *testing.T) {
	if err := Setup(&config.LoggingConfig{Level: "debug"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := Setup(&config.LoggingConfig{Level: ""}); err != nil {
		t.Fatalf("empty level should default to info: %v", err)
	}
	if err := Setup(&config.LoggingConfig{Level: "bogus"}); err == nil {
		t.Fatal("expected error for invalid level")
	}
}

// Level names from the zap-based releases must keep working, because an
// existing LOGGING_LEVEL=fatal would otherwise turn into a startup failure on
// upgrade. They map to error, the highest level the service logs at.
func TestParseLevelLegacyNames(t *testing.T) {
	for _, name := range []string{"fatal", "PANIC", "dpanic"} {
		level, err := parseLevel(name)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if level != slog.LevelError {
			t.Errorf("%s: expected error level, got %v", name, level)
		}
	}
	for name, want := range map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"ERROR": slog.LevelError,
	} {
		level, err := parseLevel(name)
		if err != nil || level != want {
			t.Errorf("%s: got %v, %v; want %v", name, level, err, want)
		}
	}
}
