// Package config loads the service configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Config is the validated service configuration.
type Config struct {
	Port            int
	DatabaseURL     string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
	MaxBodyBytes    int64
}

const (
	defaultPort            = 8080
	defaultLogLevel        = slog.LevelInfo
	defaultShutdownTimeout = 20 * time.Second
	defaultMaxBodyBytes    = 1 << 20 // 1 MiB
)

// Load reads the configuration through getenv (os.Getenv in production) and
// returns every problem it finds at once, so a bad deployment fails on start.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Port:            defaultPort,
		LogLevel:        defaultLogLevel,
		ShutdownTimeout: defaultShutdownTimeout,
		MaxBodyBytes:    defaultMaxBodyBytes,
	}
	var errs []error

	if v := getenv("PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil || port < 1 || port > 65535 {
			errs = append(errs, fmt.Errorf("PORT: %q is not a port number (1-65535)", v))
		} else {
			cfg.Port = port
		}
	}

	cfg.DatabaseURL = strings.TrimSpace(getenv("DATABASE_URL"))
	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL: required"))
	}

	if v := getenv("LOG_LEVEL"); v != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			errs = append(errs, fmt.Errorf("LOG_LEVEL: %q is not one of debug, info, warn, error", v))
		}
	}

	if v := getenv("SHUTDOWN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("SHUTDOWN_TIMEOUT: %q is not a positive duration (e.g. 20s)", v))
		} else {
			cfg.ShutdownTimeout = d
		}
	}

	if v := getenv("MAX_BODY_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			errs = append(errs, fmt.Errorf("MAX_BODY_BYTES: %q is not a positive integer", v))
		} else {
			cfg.MaxBodyBytes = n
		}
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %w", errors.Join(errs...))
	}
	return cfg, nil
}
