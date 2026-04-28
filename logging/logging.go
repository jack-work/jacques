package logging

import (
	"context"
	"os"
	"path/filepath"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

type Shutdown func()

func Init(serviceName string) (Shutdown, error) {
	logDir := filepath.Join(os.Getenv("APPDATA"), "jacques")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return func() {}, err
	}

	logFile, err := os.OpenFile(
		filepath.Join(logDir, "jacques.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0o644,
	)
	if err != nil {
		return func() {}, err
	}

	res, err := resource.New(
		context.Background(),
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		logFile.Close()
		return func() {}, err
	}

	logExporter, err := stdoutlog.New(stdoutlog.WithWriter(logFile))
	if err != nil {
		logFile.Close()
		return func() {}, err
	}

	logProvider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewSimpleProcessor(logExporter)),
	)
	global.SetLoggerProvider(logProvider)

	traceExporter, err := stdouttrace.New(stdouttrace.WithWriter(logFile))
	if err != nil {
		logFile.Close()
		return func() {}, err
	}

	traceProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSyncer(traceExporter),
	)
	otel.SetTracerProvider(traceProvider)

	return func() {
		ctx := context.Background()
		logProvider.Shutdown(ctx)
		traceProvider.Shutdown(ctx)
		logFile.Close()
	}, nil
}
