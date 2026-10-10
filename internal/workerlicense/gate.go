package workerlicense

import (
	"context"
	"errors"
	core "github.com/J-S-Te/license-core"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/commercial"
)

type contextKey struct{}

var ErrDenied = errors.New("commercial license task deferred")

func Start(ctx context.Context, app string) (context.Context, error) {
	check, err := commercial.Start(ctx, app)
	if err != nil {
		return nil, err
	}
	return WithCheck(ctx, check), nil
}
func WithCheck(ctx context.Context, check commercial.Check) context.Context {
	return context.WithValue(ctx, contextKey{}, check)
}
func Checker(ctx context.Context) commercial.Check {
	check, _ := ctx.Value(contextKey{}).(commercial.Check)
	return check
}
func Require(ctx context.Context, op core.Operation) error {
	return RequireCheck(ctx, Checker(ctx), op)
}
func RequireCheck(ctx context.Context, check commercial.Check, op core.Operation) error {
	if check != nil && check(ctx, op) != nil {
		return ErrDenied
	}
	return nil
}
func Allowed(ctx context.Context, op core.Operation) bool { return Require(ctx, op) == nil }
