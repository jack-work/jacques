package logging

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
)

const instrumentationName = "jacques"

func Info(ctx context.Context, msg string, attrs ...log.KeyValue) {
	emit(ctx, log.SeverityInfo, msg, attrs...)
}

func Warn(ctx context.Context, msg string, attrs ...log.KeyValue) {
	emit(ctx, log.SeverityWarn, msg, attrs...)
}

func Error(ctx context.Context, msg string, attrs ...log.KeyValue) {
	emit(ctx, log.SeverityError, msg, attrs...)
}

func Debug(ctx context.Context, msg string, attrs ...log.KeyValue) {
	emit(ctx, log.SeverityDebug, msg, attrs...)
}

func Infof(ctx context.Context, format string, args ...interface{}) {
	Info(ctx, fmt.Sprintf(format, args...))
}

func Errorf(ctx context.Context, format string, args ...interface{}) {
	Error(ctx, fmt.Sprintf(format, args...))
}

func Debugf(ctx context.Context, format string, args ...interface{}) {
	Debug(ctx, fmt.Sprintf(format, args...))
}

func emit(ctx context.Context, sev log.Severity, msg string, attrs ...log.KeyValue) {
	logger := global.GetLoggerProvider().Logger(instrumentationName)
	var rec log.Record
	rec.SetSeverity(sev)
	rec.SetBody(log.StringValue(msg))
	if len(attrs) > 0 {
		rec.AddAttributes(attrs...)
	}
	logger.Emit(ctx, rec)
}

func String(key, val string) log.KeyValue {
	return log.String(key, val)
}

func Int(key string, val int) log.KeyValue {
	return log.Int(key, val)
}

func Int64(key string, val int64) log.KeyValue {
	return log.Int64(key, val)
}
