package tools

import "context"

type scopeKey struct{}

// WithScope attaches an immutable request Scope. V1 stores only WorkingDir.
func WithScope(ctx context.Context, scope Scope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, scopeKey{}, scope)
}

func scopeFrom(ctx context.Context) Scope {
	if ctx == nil {
		return Scope{}
	}
	scope, _ := ctx.Value(scopeKey{}).(Scope)
	return scope
}

type reentryKey struct{}

func withExecutor(ctx context.Context, exec *Executor) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, reentryKey{}, exec)
}

func executorFrom(ctx context.Context) *Executor {
	if ctx == nil {
		return nil
	}
	exec, _ := ctx.Value(reentryKey{}).(*Executor)
	return exec
}
