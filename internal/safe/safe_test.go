package safe

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestDo(t *testing.T) {
	t.Run("returns nil when fn completes", func(t *testing.T) {
		ran := false
		if err := Do(testLogger(), "work", func() { ran = true }); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ran {
			t.Fatal("fn was not called")
		}
	})

	t.Run("converts a panic into a PanicError", func(t *testing.T) {
		err := Do(testLogger(), "work", func() { panic("boom") })
		if err == nil {
			t.Fatal("expected an error from a panicking fn")
		}

		var pe *PanicError
		if !errors.As(err, &pe) {
			t.Fatalf("error %v is not a *PanicError", err)
		}
		if pe.Value != "boom" {
			t.Fatalf("PanicError.Value = %v, want %q", pe.Value, "boom")
		}
		if !strings.Contains(err.Error(), "boom") {
			t.Fatalf("error message %q does not mention the panic value", err.Error())
		}
		if len(pe.Stack) == 0 {
			t.Fatal("PanicError.Stack is empty")
		}
	})

	t.Run("recovers a runtime panic", func(t *testing.T) {
		var p *int
		err := Do(testLogger(), "work", func() { _ = *p })
		if err == nil {
			t.Fatal("expected an error from a nil dereference")
		}
	})

	t.Run("tolerates a nil logger", func(t *testing.T) {
		if err := Do(nil, "work", func() { panic("boom") }); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestGoRecoversWithoutCrashing(t *testing.T) {
	done := make(chan struct{})
	Go(testLogger(), "work", func() {
		defer close(done)
		panic("boom")
	})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("goroutine did not run")
	}

	// Reaching here at all means the panic did not propagate and kill the
	// test binary, which is the whole point of the helper.
}
