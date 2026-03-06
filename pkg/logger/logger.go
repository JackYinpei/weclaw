package logger

import (
	"os"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	log  *zap.SugaredLogger
	once sync.Once
)

// Init initializes the global logger.
func Init(level string) {
	once.Do(func() {
		var zapLevel zapcore.Level
		switch level {
		case "debug":
			zapLevel = zapcore.DebugLevel
		case "info":
			zapLevel = zapcore.InfoLevel
		case "warn":
			zapLevel = zapcore.WarnLevel
		case "error":
			zapLevel = zapcore.ErrorLevel
		default:
			zapLevel = zapcore.InfoLevel
		}

		encoderConfig := zapcore.EncoderConfig{
			TimeKey:        "time",
			LevelKey:       "level",
			NameKey:        "logger",
			CallerKey:      "caller",
			MessageKey:     "msg",
			StacktraceKey:  "stacktrace",
			LineEnding:     zapcore.DefaultLineEnding,
			EncodeLevel:    zapcore.CapitalColorLevelEncoder,
			EncodeTime:     zapcore.ISO8601TimeEncoder,
			EncodeDuration: zapcore.StringDurationEncoder,
			EncodeCaller:   zapcore.ShortCallerEncoder,
		}

		core := zapcore.NewCore(
			zapcore.NewConsoleEncoder(encoderConfig),
			zapcore.AddSync(os.Stdout),
			zapLevel,
		)

		zapLogger := zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))
		log = zapLogger.Sugar()
	})
}

// L returns the global sugared logger.
func L() *zap.SugaredLogger {
	if log == nil {
		Init("info")
	}
	return log
}

// Debug logs a debug message.
func Debug(msg string, keysAndValues ...interface{}) {
	L().Debugw(msg, keysAndValues...)
}

// Info logs an info message.
func Info(msg string, keysAndValues ...interface{}) {
	L().Infow(msg, keysAndValues...)
}

// Warn logs a warning message.
func Warn(msg string, keysAndValues ...interface{}) {
	L().Warnw(msg, keysAndValues...)
}

// Error logs an error message.
func Error(msg string, keysAndValues ...interface{}) {
	L().Errorw(msg, keysAndValues...)
}

// Fatal logs a fatal message and exits.
func Fatal(msg string, keysAndValues ...interface{}) {
	L().Fatalw(msg, keysAndValues...)
}
