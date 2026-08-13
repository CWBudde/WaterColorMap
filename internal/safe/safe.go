// Package safe provides panic recovery for background goroutines.
//
// net/http recovers panics in handler goroutines, but nothing recovers a panic
// in a goroutine started with a bare `go`. In a long-running tile server a
// single malformed upstream response would otherwise take the whole process
// down, so every background worker runs its unit of work through Do.
package safe

import (
	"fmt"
	"log/slog"
	"runtime/debug"
)

// PanicError wraps a recovered panic value so it can be handled as an ordinary
// error. The original value is available via Value for callers that need it.
type PanicError struct {
	Value any
	Stack []byte
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("panic: %v", e.Value)
}

// Do runs fn and converts a panic into a *PanicError, logging it with the
// stack trace captured at the point of the panic.
//
// Wrap the smallest useful unit of work rather than a whole worker loop: a
// recover that spans the loop leaves the goroutine dead and silently shrinks
// the pool, which is harder to notice than the crash it replaced.
func Do(logger *slog.Logger, name string, fn func()) (err error) {
	defer recoverAndLog(logger, name, &err)

	fn()
	return nil
}

// recoverAndLog must be deferred directly, so that recover() runs in the
// frame that the panic is unwinding. When out is non-nil the recovered panic
// is also reported to the caller as an error.
func recoverAndLog(logger *slog.Logger, name string, out *error) {
	r := recover()
	if r == nil {
		return
	}
	stack := debug.Stack()
	if out != nil {
		*out = &PanicError{Value: r, Stack: stack}
	}
	log(logger).Error("recovered panic in background work",
		"work", name, "panic", fmt.Sprint(r), "stack", string(stack))
}

// Go runs fn in a new goroutine, recovering and logging any panic.
//
// This is a backstop for the goroutine as a whole. Workers that process a
// stream of jobs should additionally wrap each job in Do so that one bad job
// does not end the worker.
func Go(logger *slog.Logger, name string, fn func()) {
	go func() {
		defer recoverAndLog(logger, name, nil)
		fn()
	}()
}

func log(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.Default()
}
