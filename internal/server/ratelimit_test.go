package server

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

// newTestLimiter returns a limiter driven by a caller-controlled clock, so the
// token bucket can be exercised without sleeping.
func newTestLimiter(t *testing.T, cfg RateLimitConfig) (*IPRateLimiter, func(time.Duration)) {
	t.Helper()

	l := NewIPRateLimiter(cfg)
	t.Cleanup(l.Close)

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	return l, func(d time.Duration) { now = now.Add(d) }
}

func TestIPRateLimiter(t *testing.T) {
	t.Run("burst is exhausted then refills", func(t *testing.T) {
		l, advance := newTestLimiter(t, RateLimitConfig{RPS: 1, Burst: 3})
		key := netip.MustParseAddr("192.0.2.1")

		for i := range 3 {
			if ok, _ := l.Allow(key); !ok {
				t.Fatalf("request %d should have been allowed", i+1)
			}
		}

		ok, retryAfter := l.Allow(key)
		if ok {
			t.Fatal("the fourth request should have been rejected")
		}
		if retryAfter < time.Second {
			t.Fatalf("retryAfter = %v, want at least 1s", retryAfter)
		}

		advance(time.Second)
		if ok, _ := l.Allow(key); !ok {
			t.Fatal("a token should have refilled after a second")
		}
	})

	t.Run("clients are isolated", func(t *testing.T) {
		l, _ := newTestLimiter(t, RateLimitConfig{RPS: 1, Burst: 1})
		a := netip.MustParseAddr("192.0.2.1")
		b := netip.MustParseAddr("192.0.2.2")

		if ok, _ := l.Allow(a); !ok {
			t.Fatal("first client should be allowed")
		}
		if ok, _ := l.Allow(a); ok {
			t.Fatal("first client should now be limited")
		}
		if ok, _ := l.Allow(b); !ok {
			t.Fatal("a different client must not inherit the first client's bucket")
		}
	})

	t.Run("rejected requests never reach the handler", func(t *testing.T) {
		l, _ := newTestLimiter(t, RateLimitConfig{RPS: 1, Burst: 2})

		calls := 0
		h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.WriteHeader(http.StatusOK)
		}))

		var last *httptest.ResponseRecorder
		for range 3 {
			last = httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/tiles/z1_x0_y0.png", nil)
			req.RemoteAddr = "192.0.2.9:1234"
			h.ServeHTTP(last, req)
		}

		if calls != 2 {
			t.Fatalf("handler called %d times, want 2", calls)
		}
		if last.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429", last.Code)
		}
		if got := last.Header().Get("Retry-After"); got == "" {
			t.Fatal("429 response has no Retry-After header")
		}
		if got := last.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control = %q, want no-store", got)
		}
	})

}

func TestIPRateLimiterDisabledPassesThrough(t *testing.T) {
	l, _ := newTestLimiter(t, RateLimitConfig{RPS: 0})

	calls := 0
	h := l.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { calls++ }))

	for range 50 {
		req := httptest.NewRequest(http.MethodGet, "/tiles/z1_x0_y0.png", nil)
		req.RemoteAddr = "192.0.2.9:1234"
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if calls != 50 {
		t.Fatalf("handler called %d times, want 50", calls)
	}
}

// Without eviction the limiter would reproduce the unbounded-map bug it is
// meant to help contain.
func TestIPRateLimiterEviction(t *testing.T) {
	t.Run("idle entries are evicted", func(t *testing.T) {
		l, advance := newTestLimiter(t, RateLimitConfig{RPS: 1, Burst: 1, TTL: time.Minute})

		for i := range 5 {
			l.Allow(netip.MustParseAddr("192.0.2." + string(rune('1'+i))))
		}
		if got := l.Len(); got != 5 {
			t.Fatalf("Len = %d, want 5", got)
		}

		advance(2 * time.Minute)
		l.sweep(l.now())

		if got := l.Len(); got != 0 {
			t.Fatalf("Len = %d, want 0 after the TTL elapsed", got)
		}
	})

	t.Run("entry cap falls back to a shared bucket", func(t *testing.T) {
		l, _ := newTestLimiter(t, RateLimitConfig{RPS: 1, Burst: 5, TTL: time.Hour, MaxEntries: 2})

		// Beyond the cap, new clients must still be served rather than
		// erroring or growing the map: an address-spraying attacker should
		// not be able to force allocation.
		for i := range 10 {
			key := netip.AddrFrom4([4]byte{192, 0, 2, byte(i + 1)})
			if ok, _ := l.Allow(key); !ok && i < 2 {
				t.Fatalf("client %d should have its own bucket", i)
			}
		}

		if got := l.Len(); got > 2 {
			t.Fatalf("Len = %d, want at most MaxEntries (2)", got)
		}
	})

}

func TestIPRateLimiterCloseIsIdempotent(_ *testing.T) {
	l := NewIPRateLimiter(RateLimitConfig{RPS: 1, Burst: 1})
	l.Close()
	l.Close()
}

func TestProxyPolicyClientKey(t *testing.T) {
	trusted, err := ParseProxyPolicy("127.0.0.1/32, 10.0.0.0/8")
	if err != nil {
		t.Fatalf("ParseProxyPolicy: %v", err)
	}

	tests := []struct {
		name       string
		remoteAddr string
		forwarded  string
		realIP     string
		want       string
		policy     ProxyPolicy
	}{
		{
			name:       "peer address is used by default",
			remoteAddr: "203.0.113.5:5000",
			want:       "203.0.113.5",
		},
		{
			// The security-critical case: an untrusted peer must not be
			// able to choose its own bucket by setting a header.
			name:       "spoofed forwarding header from an untrusted peer is ignored",
			remoteAddr: "203.0.113.5:5000",
			forwarded:  "1.2.3.4",
			want:       "203.0.113.5",
		},
		{
			name:       "trusted proxy yields the rightmost untrusted hop",
			policy:     trusted,
			remoteAddr: "10.0.0.7:5000",
			forwarded:  "1.2.3.4, 203.0.113.9, 10.0.0.8",
			want:       "203.0.113.9",
		},
		{
			name:       "all hops trusted falls back to the peer",
			policy:     trusted,
			remoteAddr: "10.0.0.7:5000",
			forwarded:  "10.0.0.8, 127.0.0.1",
			want:       "10.0.0.7",
		},
		{
			name:       "X-Real-Ip is a fallback for a trusted peer",
			policy:     trusted,
			remoteAddr: "10.0.0.7:5000",
			realIP:     "198.51.100.4",
			want:       "198.51.100.4",
		},
		{
			name:       "IPv6 peer is truncated to its /64",
			remoteAddr: "[2001:db8::1]:5000",
			want:       "2001:db8::",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/tiles/z1_x0_y0.png", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tt.forwarded)
			}
			if tt.realIP != "" {
				req.Header.Set("X-Real-Ip", tt.realIP)
			}

			if got := tt.policy.ClientKey(req).String(); got != tt.want {
				t.Fatalf("ClientKey = %s, want %s", got, tt.want)
			}
		})
	}
}

// Two addresses in one /64 must share a bucket, or a client can rotate within
// its own prefix and defeat the limit entirely.
func TestIPv6AddressesInSamePrefixShareABucket(t *testing.T) {
	l, _ := newTestLimiter(t, RateLimitConfig{RPS: 1, Burst: 1})

	var policy ProxyPolicy
	keyFor := func(remoteAddr string) netip.Addr {
		req := httptest.NewRequest(http.MethodGet, "/tiles/z1_x0_y0.png", nil)
		req.RemoteAddr = remoteAddr
		return policy.ClientKey(req)
	}

	if ok, _ := l.Allow(keyFor("[2001:db8::1]:5000")); !ok {
		t.Fatal("first request should be allowed")
	}
	if ok, _ := l.Allow(keyFor("[2001:db8::2]:5000")); ok {
		t.Fatal("a second address in the same /64 must share the bucket")
	}
	if ok, _ := l.Allow(keyFor("[2001:db9::1]:5000")); !ok {
		t.Fatal("a different /64 should have its own bucket")
	}
}
