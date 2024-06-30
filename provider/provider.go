package provider

import (
	"os"

	"go.uber.org/zap"
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
	logger, err := zap.NewProduction(zap.WithCaller(true), zap.AddStacktrace(zap.ErrorLevel))
	if err != nil {
		return nil, err
	}

	config, err := NewConfig()
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(config.StoragePath, 0755); err != nil {
		return nil, err
	}

	return &provider{
		config: config,
		logger: logger,
	}, nil
}

func (p *provider) Config() *Config {
	return p.config
}

func (p *provider) Logger() *zap.Logger {
	return p.logger
}
