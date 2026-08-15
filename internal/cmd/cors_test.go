package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// okHandler records whether the wrapped handler was reached and, when it is,
// answers 200. Preflight handling must never reach it while CORS is on.
func okHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

// CORS defaults to off, so no cross-origin header may be emitted unless
// --cors-origin was given. There was no test for any of this before, and the
// headers used to be hardcoded in four separate places.
func TestWithCORSDisabledEmitsNoHeaders(t *testing.T) {
	reached := false
	h := withCORS("", okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/tiles/z13_x4297_y2754.png", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !reached {
		t.Fatal("request did not reach the wrapped handler")
	}
	for _, header := range []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Headers",
	} {
		if got := rec.Header().Get(header); got != "" {
			t.Errorf("%s = %q, want it absent while CORS is off", header, got)
		}
	}
}

// With CORS off the middleware must not swallow the preflight either: there is
// no policy to advertise, so the request is just passed through.
func TestWithCORSDisabledPassesPreflightThrough(t *testing.T) {
	reached := false
	h := withCORS("", okHandler(&reached))

	req := httptest.NewRequest(http.MethodOptions, "/tiles/z13_x4297_y2754.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !reached {
		t.Fatal("preflight did not reach the wrapped handler")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want it absent while CORS is off", got)
	}
}

func TestWithCORSEnabledSetsHeaders(t *testing.T) {
	tests := []struct {
		name      string
		origin    string
		wantAllow string
		wantVary  bool
	}{
		{name: "wildcard", origin: "*", wantAllow: "*"},
		{name: "specific origin", origin: "https://example.com", wantAllow: "https://example.com", wantVary: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			h := withCORS(tt.origin, okHandler(&reached))

			req := httptest.NewRequest(http.MethodGet, "/tiles/z13_x4297_y2754.png", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if !reached {
				t.Fatal("request did not reach the wrapped handler")
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tt.wantAllow {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tt.wantAllow)
			}
			if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "GET, OPTIONS" {
				t.Errorf("Access-Control-Allow-Methods = %q, want %q", got, "GET, OPTIONS")
			}
			if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type" {
				t.Errorf("Access-Control-Allow-Headers = %q, want %q", got, "Content-Type")
			}
			if gotVary := rec.Header().Get("Vary") == "Origin"; gotVary != tt.wantVary {
				t.Errorf("Vary: Origin present = %v, want %v", gotVary, tt.wantVary)
			}
		})
	}
}

// The middleware owns preflights so that the handlers below it can stay free of
// CORS entirely.
func TestWithCORSEnabledAnswersPreflight(t *testing.T) {
	reached := false
	h := withCORS("*", okHandler(&reached))

	req := httptest.NewRequest(http.MethodOptions, "/tiles/z13_x4297_y2754.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if reached {
		t.Fatal("preflight reached the wrapped handler; the middleware must answer it")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "*")
	}
}

// The tile handlers check no method themselves, and with CORS disabled withCORS
// passes everything straight through — so before withReadMethods an OPTIONS on
// a missing tile ran the whole on-demand fetch-and-render path.
func TestWithReadMethodsRejectsNonReadMethods(t *testing.T) {
	var called bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		method     string
		wantStatus int
		wantCalled bool
	}{
		{http.MethodGet, http.StatusOK, true},
		{http.MethodHead, http.StatusOK, true},
		{http.MethodOptions, http.StatusMethodNotAllowed, false},
		{http.MethodPost, http.StatusMethodNotAllowed, false},
		{http.MethodDelete, http.StatusMethodNotAllowed, false},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			called = false
			rec := httptest.NewRecorder()
			// CORS off: the guard is the only thing standing in front.
			withCORS("", withReadMethods(inner)).ServeHTTP(
				rec, httptest.NewRequest(tt.method, "/tiles/z13_x4317_y2692.png", nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if called != tt.wantCalled {
				t.Errorf("inner handler called = %v, want %v", called, tt.wantCalled)
			}
		})
	}
}

// With CORS enabled the preflight must still be answered, not 405'd.
func TestWithReadMethodsKeepsCORSPreflight(t *testing.T) {
	inner := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("inner handler must not run for a preflight")
	})

	rec := httptest.NewRecorder()
	withCORS("*", withReadMethods(inner)).ServeHTTP(
		rec, httptest.NewRequest(http.MethodOptions, "/tiles/z13_x4317_y2692.png", nil))

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}
