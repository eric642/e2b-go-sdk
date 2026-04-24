package template

import (
	"fmt"
	"io"
	"os"
	"time"
)

// LogLevel mirrors the upstream log severity enum.
type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// LogEntry is one structured line from a template build.
type LogEntry struct {
	Level     LogLevel
	Message   string
	Timestamp time.Time
}

// LogEntryStart marks the beginning of a build phase.
type LogEntryStart struct {
	Phase string
}

// LogEntryEnd marks the end of a build phase.
type LogEntryEnd struct {
	Phase string
	OK    bool
}

// DefaultLogger returns a function that prints log entries to stderr.
func DefaultLogger(w io.Writer) func(LogEntry) {
	if w == nil {
		w = os.Stderr
	}
	return func(e LogEntry) {
		fmt.Fprintf(w, "[%s] %s %s\n", e.Timestamp.Format(time.RFC3339), e.Level, e.Message)
	}
}
