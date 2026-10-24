package observability

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

type teeHandler struct{ left, right slog.Handler }

func (h teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.left.Enabled(ctx, level) || h.right.Enabled(ctx, level)
}

func (h teeHandler) Handle(ctx context.Context, record slog.Record) error {
	var first, second error
	if h.left.Enabled(ctx, record.Level) {
		first = h.left.Handle(ctx, record)
	}
	if h.right.Enabled(ctx, record.Level) {
		second = h.right.Handle(ctx, record)
	}
	return errors.Join(first, second)
}

func (h teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return teeHandler{h.left.WithAttrs(attrs), h.right.WithAttrs(attrs)}
}

func (h teeHandler) WithGroup(name string) slog.Handler {
	return teeHandler{h.left.WithGroup(name), h.right.WithGroup(name)}
}

func InitLogging(ctx context.Context, service string) (func(context.Context) error, error) {
	stdout := slog.NewJSONHandler(os.Stdout, nil)
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		slog.SetDefault(slog.New(stdout))
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlploghttp.New(ctx)
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(service)))
	if err != nil {
		return nil, err
	}
	provider := sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)))
	remote := otelslog.NewHandler(service, otelslog.WithLoggerProvider(provider))
	slog.SetDefault(slog.New(teeHandler{left: stdout, right: remote}))
	return provider.Shutdown, nil
}
