package estoria

import "log/slog"

//nolint:gochecknoglobals // The logger is a global component
var logger Logger

//nolint:gochecknoinits // The logger is a global component
func init() {
	logger = DefaultLogger()
}

type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

//nolint:ireturn // Deliberately an interface
func DefaultLogger() Logger {
	return slog.Default()
}

//nolint:ireturn // Deliberately an interface
func GetLogger() Logger {
	return logger
}

func SetLogger(l Logger) {
	logger = l
}
