package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/debswarm/debswarm/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// setupLogger creates a configured zap logger based on global flags.
func setupLogger() (*zap.Logger, error) {
	level := zapcore.InfoLevel
	switch logLevel {
	case "debug":
		level = zapcore.DebugLevel
	case "warn":
		level = zapcore.WarnLevel
	case "error":
		level = zapcore.ErrorLevel
	}

	cfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(level),
		Development:      false,
		Encoding:         "console",
		EncoderConfig:    zap.NewDevelopmentEncoderConfig(),
		OutputPaths:      []string{"stderr"},
		ErrorOutputPaths: []string{"stderr"},
	}

	if logFile != "" {
		cfg.OutputPaths = []string{logFile}
	}

	return cfg.Build()
}

// configPaths returns the list of config file paths to search.
func configPaths() []string {
	if cfgFile != "" {
		return []string{cfgFile}
	}
	homeDir, _ := os.UserHomeDir()
	return []string{
		"/etc/debswarm/config.toml",
		filepath.Join(homeDir, ".config", "debswarm", "config.toml"),
	}
}

// loadConfig loads configuration from the first available config file.
func loadConfig() (*config.Config, error) {
	cfg, _, err := loadConfigWithWarnings()
	return cfg, err
}

// loadConfigWithWarnings loads config and returns security warnings for sensitive settings.
func loadConfigWithWarnings() (*config.Config, []config.SecurityWarning, error) {
	for _, path := range configPaths() {
		if _, err := os.Stat(path); err == nil {
			return config.LoadWithWarnings(path)
		}
	}
	return config.DefaultConfig(), nil, nil
}

// formatBytes formats a byte count as a human-readable string.
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
