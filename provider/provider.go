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
	config, err := NewConfig()
	if err != nil {
		return nil, err
	}

	logger, err := newLogger(config.LogPath)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(config.StoragePath, 0o755); err != nil {
		return nil, err
	}

	return &provider{config: config, logger: logger}, nil
}

func newLogger(logPath string) (*zap.Logger, error) {
	if err := os.MkdirAll(logPath, 0o755); err != nil {
		return nil, err
	}

	logFile := filepath.Join(logPath, "vee.log")
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}

	fileSink := zapcore.AddSync(f)
	stderrSink := zapcore.AddSync(os.Stderr)

	encCfg := zap.NewProductionEncoderConfig()
	encCfg.TimeKey = "ts"
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	fileCore := zapcore.NewCore(
		zapcore.NewJSONEncoder(encCfg),
		fileSink,
		zapcore.InfoLevel,
	)
	consoleCore := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encCfg),
		stderrSink,
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
