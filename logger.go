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
	With(args ...any) Logger
	WithGroup(group string) Logger
}

type SlogLogger struct {
	*slog.Logger
}

func (l SlogLogger) With(args ...any) Logger {
	return l.With(args...)
}

func (l SlogLogger) WithGroup(group string) Logger {
	return l.WithGroup(group)
}

func DefaultLogger() SlogLogger {
	return SlogLogger{slog.Default()}
}

//nolint:ireturn // Deliberately an interface
func GetLogger() Logger {
	return logger
}

func SetLogger(l Logger) {
	logger = l
}
