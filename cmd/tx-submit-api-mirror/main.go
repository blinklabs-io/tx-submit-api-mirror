// Copyright 2023 Blink Labs Software
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

package main

import (
	"flag"
	"fmt"
	"os"

	_ "go.uber.org/automaxprocs"

	"github.com/blinklabs-io/tx-submit-api-mirror/api"
	"github.com/blinklabs-io/tx-submit-api-mirror/config"
	"github.com/blinklabs-io/tx-submit-api-mirror/logging"
)

var cmdlineFlags struct {
	configFile string
}

func main() {
	flag.StringVar(
		&cmdlineFlags.configFile,
		"config",
		"",
		"path to config file to load",
	)
	flag.Parse()

	// Load config
	cfg, err := config.Load(cmdlineFlags.configFile)
	if err != nil {
		fmt.Printf("Failed to load config: %s\n", err)
		os.Exit(1)
	}

	// Configure logging
	logging.Setup(&cfg.Logging)
	logger := logging.GetLogger()
	// Sync logger on exit
	defer func() {
		if err := logger.Sync(); err != nil {
			// We don't actually care about the error here, but we have to do something
			// to appease the linter
			return
		}
	}()

	// Start API listener
	logger.Infof(
		"starting API listener on %s:%d",
		cfg.Api.ListenAddress,
		cfg.Api.ListenPort,
	)
	if err := api.Start(cfg); err != nil {
		logger.Fatalf("failed to start API: %s", err)
	}

	// Wait forever
	select {}
}
