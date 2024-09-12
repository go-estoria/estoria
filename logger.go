package estoria

import "log/slog"

var logger Logger

func init() {
	logger = DefaultLogger()
}

type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

func DefaultLogger() Logger {
	return slog.Default()
}

func GetLogger() Logger {
	return logger
}

func SetLogger(l Logger) {
	logger = l
}
