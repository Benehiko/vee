package provider

import (
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Provider interface {
	Config() *Config
	Logger() *zap.Logger
}

type provider struct {
	config *Config
	logger *zap.Logger
}

func NewProvider() (Provider, error) {
	return newProvider(false)
}

// NewProviderSilent returns a Provider that logs only to file, not stderr.
// Use this when a TUI owns the terminal.
func NewProviderSilent() (Provider, error) {
	return newProvider(true)
}

func newProvider(silent bool) (Provider, error) {
	config, err := NewConfig()
	if err != nil {
		return nil, err
	}

	logger, err := newLogger(config.LogPath, silent)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(config.StoragePath, 0o755); err != nil {
		return nil, err
	}

	return &provider{config: config, logger: logger}, nil
}

func newLogger(logPath string, silent bool) (*zap.Logger, error) {
	if err := os.MkdirAll(logPath, 0o755); err != nil {
		return nil, err
	}

	logFile := filepath.Join(logPath, "vee.log")
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}

	encCfg := zap.NewProductionEncoderConfig()
	encCfg.TimeKey = "ts"
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	fileCore := zapcore.NewCore(
		zapcore.NewJSONEncoder(encCfg),
		zapcore.AddSync(f),
		zapcore.InfoLevel,
	)

	if silent {
		return zap.New(fileCore, zap.AddCaller()), nil
	}

	consoleCore := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encCfg),
		zapcore.AddSync(os.Stderr),
		zapcore.InfoLevel,
	)

	return zap.New(zapcore.NewTee(fileCore, consoleCore), zap.AddCaller()), nil
}

func (p *provider) Config() *Config {
	return p.config
}

func (p *provider) Logger() *zap.Logger {
	return p.logger
}
