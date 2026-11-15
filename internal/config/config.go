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
	LogExportLevel  slog.Level // minimum level exported over OTLP; LogLevel applies to stdout
	ShutdownTimeout time.Duration
	MaxBodyBytes    int64
}

const (
	defaultPort            = 8080
	defaultLogLevel        = slog.LevelInfo
	defaultLogExportLevel  = slog.LevelInfo
	defaultShutdownTimeout = 20 * time.Second
	defaultMaxBodyBytes    = 1 << 20 // 1 MiB
)

// Load reads the configuration through getenv (os.Getenv in production) and
// returns every problem it finds at once, so a bad deployment fails on start.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Port:            defaultPort,
		LogLevel:        defaultLogLevel,
		LogExportLevel:  defaultLogExportLevel,
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

	for _, l := range []struct {
		name string
		dst  *slog.Level
	}{{"LOG_LEVEL", &cfg.LogLevel}, {"LOG_EXPORT_LEVEL", &cfg.LogExportLevel}} {
		if v := getenv(l.name); v != "" {
			if err := l.dst.UnmarshalText([]byte(v)); err != nil {
				errs = append(errs, fmt.Errorf("%s: %q is not one of debug, info, warn, error", l.name, v))
			}
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
