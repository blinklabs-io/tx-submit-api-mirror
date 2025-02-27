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
	"fmt"
	"os"

	"github.com/kelseyhightower/envconfig"
	"gopkg.in/yaml.v2"
)

type Config struct {
	Api      ApiConfig     `yaml:"api"`
	Logging  LoggingConfig `yaml:"logging"`
	Maestro  MaestroConfig `yaml:"maestro"`
	Tls      TlsConfig     `yaml:"tls"`
	Backends []string      `yaml:"backends" envconfig:"BACKENDS"`
}

type ApiConfig struct {
	ListenAddress string `yaml:"address"        envconfig:"API_LISTEN_ADDRESS"`
	ListenPort    uint   `yaml:"port"           envconfig:"API_LISTEN_PORT"`
	ClientTimeout uint   `yaml:"client_timeout" envconfig:"CLIENT_TIMEOUT"`
}

type LoggingConfig struct {
	Level string `yaml:"level" envconfig:"LOGGING_LEVEL"`
}

type MaestroConfig struct {
	ApiKey  string `yaml:"apiKey"  envconfig:"MAESTRO_API_KEY"`
	Network string `yaml:"network" envconfig:"MAESTRO_NETWORK"`
	TurboTx bool   `yaml:"turboTx" envconfig:"MAESTRO_TURBO_TX"`
}

type TlsConfig struct {
	CertFilePath string `yaml:"certFilePath" envconfig:"TLS_CERT_FILE_PATH"`
	KeyFilePath  string `yaml:"keyFilePath"  envconfig:"TLS_KEY_FILE_PATH"`
}

// Singleton config instance with default values
var globalConfig = &Config{
	Api: ApiConfig{
		ListenAddress: "",
		ListenPort:    8090,
		ClientTimeout: 60000, // [ms]
	},
	Logging: LoggingConfig{
		Level: "info",
	},
	Maestro: MaestroConfig{
		Network: "mainnet",
		TurboTx: false,
	},
}

func Load(configFile string) (*Config, error) {
	// Load config file as YAML if provided
	if configFile != "" {
		buf, err := os.ReadFile(configFile)
		if err != nil {
			return nil, fmt.Errorf("error reading config file: %w", err)
		}
		err = yaml.Unmarshal(buf, globalConfig)
		if err != nil {
			return nil, fmt.Errorf("error parsing config file: %w", err)
		}
	}
	// Load config values from environment variables
	// We use "dummy" as the app name here to (mostly) prevent picking up env
	// vars that we hadn't explicitly specified in annotations above
	err := envconfig.Process("dummy", globalConfig)
	if err != nil {
		return nil, fmt.Errorf("error processing environment: %w", err)
	}
	return globalConfig, nil
}

// Return global config instance
func GetConfig() *Config {
	return globalConfig
}
