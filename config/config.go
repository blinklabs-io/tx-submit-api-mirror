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

package config

import (
	"fmt"
	"github.com/kelseyhightower/envconfig"
	"gopkg.in/yaml.v2"
	"os"
)

type Config struct {
	Logging  LoggingConfig `yaml:"logging"`
	Api      ApiConfig     `yaml:"api"`
	Backends []string      `yaml:"backends" envconfig:"BACKENDS"`
}

type LoggingConfig struct {
	Level string `yaml:"level" envconfig:"LOGGING_LEVEL"`
}

type ApiConfig struct {
	ListenAddress string `yaml:"address" envconfig:"API_LISTEN_ADDRESS"`
	ListenPort    uint   `yaml:"port"    envconfig:"API_LISTEN_PORT"`
}

// Singleton config instance with default values
var globalConfig = &Config{
	Logging: LoggingConfig{
		Level: "info",
	},
	Api: ApiConfig{
		ListenAddress: "",
		ListenPort:    8090,
	},
}

func Load(configFile string) (*Config, error) {
	// Load config file as YAML if provided
	if configFile != "" {
		buf, err := os.ReadFile(configFile)
		if err != nil {
			return nil, fmt.Errorf("error reading config file: %s", err)
		}
		err = yaml.Unmarshal(buf, globalConfig)
		if err != nil {
			return nil, fmt.Errorf("error parsing config file: %s", err)
		}
	}
	// Load config values from environment variables
	// We use "dummy" as the app name here to (mostly) prevent picking up env
	// vars that we hadn't explicitly specified in annotations above
	err := envconfig.Process("dummy", globalConfig)
	if err != nil {
		return nil, fmt.Errorf("error processing environment: %s", err)
	}
	return globalConfig, nil
}

// Return global config instance
func GetConfig() *Config {
	return globalConfig
}
