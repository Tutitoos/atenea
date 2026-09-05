package checkpoint

import "context"

type correlationKey struct{}
type correlation struct{ request, run string }

// WithRequestID attaches the request shared by checkpoints and statistics.
func WithRequestID(ctx context.Context, id string) context.Context {
	ids, _ := ctx.Value(correlationKey{}).(correlation)
	ids.request = id
	return context.WithValue(ctx, correlationKey{}, ids)
}

// WithRunID attaches the run that owns the checkpoint.
func WithRunID(ctx context.Context, id string) context.Context {
	ids, _ := ctx.Value(correlationKey{}).(correlation)
	ids.run = id
	return context.WithValue(ctx, correlationKey{}, ids)
}

// RequestID returns the attached request identifier, or an empty string.
func RequestID(ctx context.Context) string {
	ids, _ := ctx.Value(correlationKey{}).(correlation)
	return ids.request
}

// RunID returns the attached run identifier, or an empty string.
func RunID(ctx context.Context) string {
	ids, _ := ctx.Value(correlationKey{}).(correlation)
	return ids.run
}
