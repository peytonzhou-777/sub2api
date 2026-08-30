package service

import (
	"context"

	"github.com/gin-gonic/gin"
)

// clearedSessionPersonaBindingContext masks the private Persona binding key
// while delegating cancellation, deadlines, and all unrelated values to the
// original request context. A context value cannot be deleted with
// context.WithValue, so a small Value wrapper is required when a request is
// reused after a failed account/slot attempt.
type clearedSessionPersonaBindingContext struct {
	context.Context
}

func (c clearedSessionPersonaBindingContext) Value(key any) any {
	if _, ok := key.(sessionPersonaBindingContextKey); ok {
		return nil
	}
	if c.Context == nil {
		return nil
	}
	return c.Context.Value(key)
}

// SessionPersonaBindingFromGin reads the typed binding attached to a Gin
// request. A missing binding intentionally selects the legacy strict-Codex
// path until the handler's new-root mapper is enabled.
func SessionPersonaBindingFromGin(c *gin.Context) (SessionPersonaSlotBinding, bool) {
	if c == nil || c.Request == nil {
		return SessionPersonaSlotBinding{}, false
	}
	return SessionPersonaBindingFromContext(c.Request.Context())
}

// SessionPersonaBindingFromContextOrGin resolves the immutable request binding
// from the derived context first, then falls back to the Gin request context.
// The explicit context-first order keeps direct service callers and failover
// attempts aligned with the handler path without borrowing a stale Gin value.
func SessionPersonaBindingFromContextOrGin(ctx context.Context, c *gin.Context) (SessionPersonaSlotBinding, bool) {
	if binding, ok := SessionPersonaBindingFromContext(ctx); ok {
		return binding, true
	}
	return SessionPersonaBindingFromGin(c)
}

// AttachSessionPersonaBindingToGin replaces the request context with an
// immutable Persona binding while preserving the existing handler signature.
func AttachSessionPersonaBindingToGin(c *gin.Context, binding SessionPersonaSlotBinding) bool {
	if c == nil || c.Request == nil {
		return false
	}
	ctx := ContextWithSessionPersonaBinding(c.Request.Context(), binding)
	if ctx == c.Request.Context() {
		return false
	}
	c.Request = c.Request.WithContext(ctx)
	return true
}

// ClearSessionPersonaBindingFromContext removes the request-scoped Persona
// binding without disturbing cancellation, deadlines, or unrelated context
// values. It returns the original context when no binding is present.
func ClearSessionPersonaBindingFromContext(ctx context.Context) context.Context {
	if ctx == nil || ctx.Value(sessionPersonaBindingContextKey{}) == nil {
		return ctx
	}
	return clearedSessionPersonaBindingContext{Context: ctx}
}

// ClearSessionPersonaBindingFromGin clears a binding attached to a Gin
// request. It is intentionally idempotent so failover/cleanup paths can call
// it without creating an ever-growing context chain.
func ClearSessionPersonaBindingFromGin(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	original := c.Request.Context()
	if original.Value(sessionPersonaBindingContextKey{}) == nil {
		return false
	}
	cleared := ClearSessionPersonaBindingFromContext(original)
	c.Request = c.Request.WithContext(cleared)
	return true
}
