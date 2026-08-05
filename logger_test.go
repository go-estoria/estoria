package estoria_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/go-estoria/estoria"
)

// The logger is package-global, so these tests cannot run in parallel with each other or
// with anything else that reads it. They restore the original on the way out.

func TestGetLogger_DefaultsToSlog(t *testing.T) {
	// A nil logger here would panic on the first Debug call anywhere in the library, so the
	// init-time default is load-bearing rather than cosmetic.
	if estoria.GetLogger() == nil {
		t.Fatal("want a logger installed by default, got nil")
	}
}

func TestSetLogger(t *testing.T) {
	original := estoria.GetLogger()
	t.Cleanup(func() { estoria.SetLogger(original) })

	replacement := &captureLogger{}
	estoria.SetLogger(replacement)

	if estoria.GetLogger() != replacement {
		t.Error("want GetLogger to return the logger that was set")
	}

	estoria.GetLogger().Info("hello")

	if len(replacement.messages) != 1 || replacement.messages[0] != "hello" {
		t.Errorf("want the message routed to the installed logger, got %v", replacement.messages)
	}
}

// TestSlogLogger_WithAndWithGroup covers the two methods SlogLogger adds over slog.Logger.
// Both exist only to return estoria.Logger rather than *slog.Logger, and both are used
// throughout the library to tag output by component.
func TestSlogLogger_WithAndWithGroup(t *testing.T) {
	var buf bytes.Buffer

	log := estoria.SlogLogger{Logger: slog.New(slog.NewTextHandler(&buf, nil))}

	log.With("key", "value").Info("with message")

	if got := buf.String(); !strings.Contains(got, "key=value") || !strings.Contains(got, "with message") {
		t.Errorf("want the attribute carried onto the record, got %q", got)
	}

	buf.Reset()

	log.WithGroup("group").Info("group message", "key", "value")

	if got := buf.String(); !strings.Contains(got, "group.key=value") {
		t.Errorf("want the attribute namespaced under the group, got %q", got)
	}
}

func TestDefaultLogger(t *testing.T) {
	if estoria.DefaultLogger().Logger == nil {
		t.Error("want a wrapped slog logger, got nil")
	}
}

// captureLogger records the messages it is handed.
type captureLogger struct {
	messages []string
}

var _ estoria.Logger = (*captureLogger)(nil)

func (l *captureLogger) Debug(msg string, _ ...any) { l.messages = append(l.messages, msg) }
func (l *captureLogger) Info(msg string, _ ...any)  { l.messages = append(l.messages, msg) }
func (l *captureLogger) Warn(msg string, _ ...any)  { l.messages = append(l.messages, msg) }
func (l *captureLogger) Error(msg string, _ ...any) { l.messages = append(l.messages, msg) }
func (l *captureLogger) With(...any) estoria.Logger { return l }

func (l *captureLogger) WithGroup(string) estoria.Logger { return l }
