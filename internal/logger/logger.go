package logger

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

var L *zap.Logger

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorGray   = "\033[90m"
	colorWhite  = "\033[97m"
	colorGreen  = "\033[32m"
)

func levelColor(l zapcore.Level) string {
	switch l {
	case zapcore.DebugLevel:
		return colorGray
	case zapcore.InfoLevel:
		return colorGreen
	case zapcore.WarnLevel:
		return colorYellow
	case zapcore.ErrorLevel, zapcore.FatalLevel:
		return colorRed
	default:
		return colorWhite
	}
}

func consoleEncoder() zapcore.Encoder {
	return zapcore.NewConsoleEncoder(zapcore.EncoderConfig{
		TimeKey:          "T",
		LevelKey:         "L",
		NameKey:          "N",
		MessageKey:       "M",
		LineEnding:       zapcore.DefaultLineEnding,
		EncodeCaller:     zapcore.ShortCallerEncoder,
		ConsoleSeparator: " | ",
		EncodeTime: func(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
			enc.AppendString(colorGray + t.Format("15:04:05") + colorReset)
		},
		EncodeLevel: func(l zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
			enc.AppendString(levelColor(l) + fmt.Sprintf("%-5s", strings.ToUpper(l.String())) + colorReset)
		},
		EncodeName: func(name string, enc zapcore.PrimitiveArrayEncoder) {
			enc.AppendString(colorCyan + "[" + name + "]" + colorReset)
		},
	})
}

func fileEncoder() zapcore.Encoder {
	return zapcore.NewConsoleEncoder(zapcore.EncoderConfig{
		TimeKey:          "T",
		LevelKey:         "L",
		NameKey:          "N",
		MessageKey:       "M",
		LineEnding:       zapcore.DefaultLineEnding,
		EncodeCaller:     zapcore.ShortCallerEncoder,
		ConsoleSeparator: " | ",
		EncodeTime: func(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
			enc.AppendString(t.UTC().Format("2006-01-02T15:04:05Z"))
		},
		EncodeLevel: func(l zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
			enc.AppendString(fmt.Sprintf("%-5s", strings.ToUpper(l.String())))
		},
		EncodeName: func(name string, enc zapcore.PrimitiveArrayEncoder) {
			enc.AppendString("[" + name + "]")
		},
	})
}

func rotateOnStartup(filename string) error {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) || (err == nil && info.Size() == 0) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat log file: %w", err)
	}
	timestamp := time.Now().UTC().Format("2006-01-02T15-04-05Z")
	base := strings.TrimSuffix(filename, ".log")
	rotated := fmt.Sprintf("%s-%s.log", base, timestamp)
	if err := os.Rename(filename, rotated); err != nil {
		return fmt.Errorf("rotate log: %w", err)
	}
	go func() {
		src, err := os.Open(rotated)
		if err != nil {
			return
		}
		defer src.Close()
		dst, err := os.Create(rotated + ".gz")
		if err != nil {
			return
		}
		defer dst.Close()
		gz := gzip.NewWriter(dst)
		io.Copy(gz, src)
		gz.Close()
		src.Close()
		os.Remove(rotated)
	}()
	return nil
}

func Init(level string, env string, service string) error {
	if err := os.MkdirAll("logs", 0755); err != nil {
		return fmt.Errorf("create logs dir: %w", err)
	}

	mainLog := fmt.Sprintf("logs/%s.log", service)
	if err := rotateOnStartup(mainLog); err != nil {
		fmt.Fprintf(os.Stderr, "[LOGGER] startup rotation failed: %v\n", err)
	}

	var zapLevel zapcore.Level
	switch level {
	case "debug":
		zapLevel = zap.DebugLevel
	case "warn":
		zapLevel = zap.WarnLevel
	case "error":
		zapLevel = zap.ErrorLevel
	default:
		zapLevel = zap.InfoLevel
	}

	consoleCore := zapcore.NewCore(consoleEncoder(), zapcore.AddSync(os.Stdout), zapLevel)
	fileCore := zapcore.NewCore(
		fileEncoder(),
		zapcore.AddSync(&lumberjack.Logger{
			Filename:   mainLog,
			MaxSize:    100,
			MaxBackups: 30,
			MaxAge:     60,
			Compress:   true,
		}),
		zapLevel,
	)

	L = zap.New(
		zapcore.NewTee(consoleCore, fileCore),
		zap.AddCaller(),
		zap.AddCallerSkip(1),
	).Named(service)

	return nil
}

func Named(module string) *zap.Logger { return L.Named(module) }
func Info(msg string, fields ...zap.Field)  { L.Info(msg, fields...) }
func Error(msg string, fields ...zap.Field) { L.Error(msg, fields...) }
func Debug(msg string, fields ...zap.Field) { L.Debug(msg, fields...) }
func Fatal(msg string, fields ...zap.Field) { L.Fatal(msg, fields...) }
func Warn(msg string, fields ...zap.Field)  { L.Warn(msg, fields...) }
