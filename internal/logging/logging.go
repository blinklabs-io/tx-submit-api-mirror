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

package logging

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/blinklabs-io/tx-submit-api-mirror/internal/config"
)

var globalLogger = slog.Default()

// Setup configures the global logger to write JSON records to stdout at the
// configured level. It returns an error if the level is not recognized.
func Setup(cfg *config.LoggingConfig) error {
	level := slog.LevelInfo
	if cfg.Level != "" {
		if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
			return fmt.Errorf("invalid logging level %q: %w", cfg.Level, err)
		}
	}
	globalLogger = slog.New(
		slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: level,
			ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
				switch a.Key {
				case slog.TimeKey:
					a.Key = "timestamp"
					if t, ok := a.Value.Any().(time.Time); ok {
						a.Value = slog.StringValue(t.Format(time.RFC3339))
					}
				case slog.MessageKey:
					a.Key = "msg"
				}
				return a
			},
		}),
	)
	return nil
}

// GetLogger returns the global logger.
func GetLogger() *slog.Logger {
	return globalLogger
}

// GetAccessLogger returns the global logger tagged as the access log.
func GetAccessLogger() *slog.Logger {
	return globalLogger.With("type", "access")
}
