package dagbee

import (
	"fmt"
	"log"
	"os"
	"strings"
)

// Logger defines the logging interface used throughout the framework.
// Implementations can wrap zap, logrus, or any structured logger.
type Logger interface {
	Debug(msg string, keysAndValues ...interface{})
	Info(msg string, keysAndValues ...interface{})
	Warn(msg string, keysAndValues ...interface{})
	Error(msg string, keysAndValues ...interface{})
}

// StdLogger implements Logger using the standard library log package.
type StdLogger struct {
	logger *log.Logger
}

// NewStdLogger creates a Logger backed by the standard library.
func NewStdLogger() *StdLogger {
	return &StdLogger{
		logger: log.New(os.Stderr, "[dagbee] ", log.LstdFlags|log.Lmicroseconds),
	}
}

func (l *StdLogger) Debug(msg string, keysAndValues ...interface{}) {
	l.logger.Printf("DEBUG %s%s", msg, formatKV(keysAndValues))
}

func (l *StdLogger) Info(msg string, keysAndValues ...interface{}) {
	l.logger.Printf("INFO  %s%s", msg, formatKV(keysAndValues))
}

func (l *StdLogger) Warn(msg string, keysAndValues ...interface{}) {
	l.logger.Printf("WARN  %s%s", msg, formatKV(keysAndValues))
}

func (l *StdLogger) Error(msg string, keysAndValues ...interface{}) {
	l.logger.Printf("ERROR %s%s", msg, formatKV(keysAndValues))
}

func formatKV(keysAndValues []interface{}) string {
	if len(keysAndValues) == 0 {
		return ""
	}
	var sb strings.Builder
	for i := 0; i < len(keysAndValues)-1; i += 2 {
		sb.WriteString(fmt.Sprintf(" %v=%v", keysAndValues[i], keysAndValues[i+1]))
	}
	if len(keysAndValues)%2 != 0 {
		sb.WriteString(fmt.Sprintf(" %v=MISSING_VALUE", keysAndValues[len(keysAndValues)-1]))
	}
	return sb.String()
}

// noopLogger silently discards all log output. Used as the default logger.
type noopLogger struct{}

func (noopLogger) Debug(string, ...interface{}) {}
func (noopLogger) Info(string, ...interface{})  {}
func (noopLogger) Warn(string, ...interface{})  {}
func (noopLogger) Error(string, ...interface{}) {}
