package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DATABASE_URL": "postgres://u:p@localhost:5432/db"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		Port:            8080,
		DatabaseURL:     "postgres://u:p@localhost:5432/db",
		LogLevel:        slog.LevelInfo,
		LogExportLevel:  slog.LevelInfo,
		ShutdownTimeout: 20 * time.Second,
		MaxBodyBytes:    1 << 20,
	}
	if cfg != want {
		t.Errorf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"PORT":             "9090",
		"DATABASE_URL":     "postgres://x",
		"LOG_LEVEL":        "debug",
		"SHUTDOWN_TIMEOUT": "5s",
		"MAX_BODY_BYTES":   "2048",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 9090 || cfg.LogLevel != slog.LevelDebug ||
		cfg.ShutdownTimeout != 5*time.Second || cfg.MaxBodyBytes != 2048 {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

func TestLoadInvalid(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"missing database url", map[string]string{}, "DATABASE_URL: required"},
		{"blank database url", map[string]string{"DATABASE_URL": "  "}, "DATABASE_URL: required"},
		{"port not a number", map[string]string{"DATABASE_URL": "x", "PORT": "http"}, "PORT"},
		{"port out of range", map[string]string{"DATABASE_URL": "x", "PORT": "70000"}, "PORT"},
		{"port zero", map[string]string{"DATABASE_URL": "x", "PORT": "0"}, "PORT"},
		{"log level", map[string]string{"DATABASE_URL": "x", "LOG_LEVEL": "verbose"}, "LOG_LEVEL"},
		{"log export level", map[string]string{"DATABASE_URL": "x", "LOG_EXPORT_LEVEL": "verbose"}, "LOG_EXPORT_LEVEL"},
		{"timeout no unit", map[string]string{"DATABASE_URL": "x", "SHUTDOWN_TIMEOUT": "20"}, "SHUTDOWN_TIMEOUT"},
		{"timeout negative", map[string]string{"DATABASE_URL": "x", "SHUTDOWN_TIMEOUT": "-1s"}, "SHUTDOWN_TIMEOUT"},
		{"body zero", map[string]string{"DATABASE_URL": "x", "MAX_BODY_BYTES": "0"}, "MAX_BODY_BYTES"},
		{"body not a number", map[string]string{"DATABASE_URL": "x", "MAX_BODY_BYTES": "1MiB"}, "MAX_BODY_BYTES"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(env(tt.env))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("got error %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	_, err := Load(env(map[string]string{"PORT": "x", "LOG_LEVEL": "x", "LOG_EXPORT_LEVEL": "x"}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, s := range []string{"PORT", "DATABASE_URL", "LOG_LEVEL", "LOG_EXPORT_LEVEL"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not mention %s", err, s)
		}
	}
}

func TestLoadLogExportLevel(t *testing.T) {
	for v, want := range map[string]slog.Level{
		"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError,
		"DEBUG": slog.LevelDebug,
	} {
		t.Run(v, func(t *testing.T) {
			cfg, err := Load(env(map[string]string{"DATABASE_URL": "x", "LOG_EXPORT_LEVEL": v}))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.LogExportLevel != want || cfg.LogLevel != slog.LevelInfo {
				t.Errorf("LogExportLevel = %v, LogLevel = %v; want %v and info", cfg.LogExportLevel, cfg.LogLevel, want)
			}
		})
	}
}
