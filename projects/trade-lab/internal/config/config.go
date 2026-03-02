package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Profile   string     `mapstructure:"profile"`
	LogLevel  string     `mapstructure:"log_level"`
	LogFormat string     `mapstructure:"log_format"`
	HTTPAddr  string     `mapstructure:"http_addr"`
	DB        DBConfig   `mapstructure:"db"`
}

type DBConfig struct {
	Driver string `mapstructure:"driver"`
	DSN    string `mapstructure:"dsn"`
}

func Load() (*Config, error) {
	return LoadWithPath("")
}

func LoadWithPath(configPath string) (*Config, error) {
	v := viper.New()

	// Set defaults
	v.SetDefault("profile", "dev")
	v.SetDefault("log_level", "info")
	v.SetDefault("log_format", "console")
	v.SetDefault("http_addr", ":8080")
	v.SetDefault("db.driver", "sqlite")
	v.SetDefault("db.dsn", "trade-lab.db")

	// Environment overrides
	v.SetEnvPrefix("TL")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Config file
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		v.AddConfigPath(".")
		v.AddConfigPath("./config")
	}

	// Read config (ignore not found)
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("read config: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// Profile-specific config
	if cfg.Profile != "" {
		profileFile := fmt.Sprintf("config.%s", cfg.Profile)
		v.SetConfigName(profileFile)
		if err := v.MergeInConfig(); err != nil {
			if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
				return nil, fmt.Errorf("merge profile config: %w", err)
			}
		}
		if err := v.Unmarshal(&cfg); err != nil {
			return nil, fmt.Errorf("unmarshal profile config: %w", err)
		}
	}

	return &cfg, nil
}
