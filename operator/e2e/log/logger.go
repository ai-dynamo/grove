// /*
// Copyright 2025 The Grove Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// */

package log

import (
	"fmt"
	"io"
	"os"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// LogLevel represents the logging verbosity level
type LogLevel int

const (
	// DebugLevel logs are typically voluminous, and are usually disabled in production.
	DebugLevel LogLevel = iota
	// InfoLevel is the default logging priority.
	InfoLevel
	// WarnLevel logs are more important than Info, but don't need individual human review.
	WarnLevel
	// ErrorLevel logs are high-priority. If an application is running smoothly, it shouldn't generate any error-level logs.
	ErrorLevel
)

// Logger provides a logrus-compatible interface backed by logr
type Logger struct {
	logr logr.Logger
}

// NewTestLogger creates a new logger with the specified verbosity level
func NewTestLogger(verbosity LogLevel) *Logger {
	return NewTestLoggerWithOutput(verbosity, os.Stdout)
}

// NewTestLoggerWithOutput creates a new logger with the specified verbosity level and output writer
func NewTestLoggerWithOutput(verbosity LogLevel, output io.Writer) *Logger {
	// Convert our LogLevel to zap level
	var zapLevel zapcore.Level
	switch verbosity {
	case DebugLevel:
		zapLevel = zapcore.DebugLevel
	case InfoLevel:
		zapLevel = zapcore.InfoLevel
	case WarnLevel:
		zapLevel = zapcore.WarnLevel
	case ErrorLevel:
		zapLevel = zapcore.ErrorLevel
	default:
		zapLevel = zapcore.InfoLevel
	}

	// Create encoder config for console-friendly output
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalColorLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// Create a console encoder
	encoder := zapcore.NewConsoleEncoder(encoderConfig)

	// Create core with our output writer
	core := zapcore.NewCore(
		encoder,
		zapcore.AddSync(output),
		zapLevel,
	)

	// Build the zap logger
	zapLogger := zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))

	// Wrap with zapr to get a logr.Logger
	logrLogger := zapr.NewLogger(zapLogger)

	return &Logger{logr: logrLogger}
}

// Debugf logs a formatted debug message
func (l *Logger) Debugf(format string, args ...interface{}) {
	l.logr.V(1).Info(fmt.Sprintf(format, args...))
}

// Debug logs a debug message
func (l *Logger) Debug(args ...interface{}) {
	l.logr.V(1).Info(fmt.Sprint(args...))
}

// Infof logs a formatted info message
func (l *Logger) Infof(format string, args ...interface{}) {
	l.logr.Info(fmt.Sprintf(format, args...))
}

// Info logs an info message
func (l *Logger) Info(args ...interface{}) {
	l.logr.Info(fmt.Sprint(args...))
}

// Warnf logs a formatted warning message
func (l *Logger) Warnf(format string, args ...interface{}) {
	l.logr.Info(fmt.Sprintf("WARN: "+format, args...))
}

// Warn logs a warning message
func (l *Logger) Warn(args ...interface{}) {
	l.logr.Info("WARN: " + fmt.Sprint(args...))
}

// Errorf logs a formatted error message
func (l *Logger) Errorf(format string, args ...interface{}) {
	l.logr.Error(nil, fmt.Sprintf(format, args...))
}

// Error logs an error message
func (l *Logger) Error(args ...interface{}) {
	l.logr.Error(nil, fmt.Sprint(args...))
}

// WriterLevel returns an io.Writer that logs at the specified level
// This is used for compatibility with code that redirects command output to logger
func (l *Logger) WriterLevel(level LogLevel) io.Writer {
	return &logWriter{logger: l, level: level}
}

// logWriter implements io.Writer to redirect output to logger
type logWriter struct {
	logger *Logger
	level  LogLevel
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	msg := string(p)
	switch w.level {
	case DebugLevel:
		w.logger.Debug(msg)
	case InfoLevel:
		w.logger.Info(msg)
	case WarnLevel:
		w.logger.Warn(msg)
	case ErrorLevel:
		w.logger.Error(msg)
	}
	return len(p), nil
}

// GetLogr returns the underlying logr.Logger.
func (l *Logger) GetLogr() logr.Logger {
	return l.logr
}

// GetLevel returns the current log level (always returns InfoLevel for now)
func (l *Logger) GetLevel() LogLevel {
	// Since logr doesn't expose the level directly, we return InfoLevel as a sensible default
	// This is mainly for compatibility with existing code
	return InfoLevel
}
