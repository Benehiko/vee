package provider

import (
	"database/sql"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/Benehiko/vee/internal/db"
)

type Provider interface {
	Config() *Config
	Logger() *zap.Logger
	DB() *sql.DB
}

type provider struct {
	config *Config
	logger *zap.Logger
	db     *sql.DB
}

// New returns a Provider. When verbose is false, info-level logs go only to
// ~/.vee/logs/vee.log; when true, they are also streamed to stderr and both
// sinks drop to debug level. CLI commands pass false by default so spinners
// and step output stay clean; pass true via the global --verbose flag for
// debugging.
func New(verbose bool) (Provider, error) {
	return newProvider(!verbose)
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

	if err := os.MkdirAll(config.StoragePath, 0o750); err != nil {
		return nil, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(home, ".vee", "vee.db")
	database, err := db.Open(dbPath, config.StoragePath)
	if err != nil {
		return nil, err
	}

	return &provider{config: config, logger: logger, db: database}, nil
}

// newLogger builds the vee logger. silent selects the sinks — file only, or
// file plus stderr — and also the level: a --verbose run is a debugging run,
// so both sinks drop to debug. Raising the file level too is deliberate; the
// log file is what ends up attached to a bug report, and the debug call sites
// (why the daemon skipped a VM, why a stop fell back to its grace period) are
// exactly what such a report needs. Non-verbose runs stay at info, so the
// shared journal does not fill with debug lines.
func newLogger(logPath string, silent bool) (*zap.Logger, error) {
	if err := os.MkdirAll(logPath, 0o750); err != nil {
		return nil, err
	}

	logFile := filepath.Join(logPath, "vee.log")
	//nolint:gosec // logFile is derived from the configured log dir, not external input.
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}

	encCfg := zap.NewProductionEncoderConfig()
	encCfg.TimeKey = "ts"
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	level := zapcore.InfoLevel
	if !silent {
		level = zapcore.DebugLevel
	}

	fileCore := zapcore.NewCore(
		zapcore.NewJSONEncoder(encCfg),
		zapcore.AddSync(f),
		level,
	)

	if silent {
		return zap.New(fileCore, zap.AddCaller()), nil
	}

	consoleCore := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encCfg),
		zapcore.AddSync(os.Stderr),
		level,
	)

	return zap.New(zapcore.NewTee(fileCore, consoleCore), zap.AddCaller()), nil
}

func (p *provider) Config() *Config {
	return p.config
}

func (p *provider) Logger() *zap.Logger {
	return p.logger
}

func (p *provider) DB() *sql.DB {
	return p.db
}
