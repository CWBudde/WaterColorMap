package server

import (
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Rate limiter defaults. These are deliberately generous: the limiter is abuse
// control, not backpressure (see OnDemandTiles.admit for the latter).
//
// A 2560x1440 viewport is roughly 60 tiles at 256px, Leaflet's keepBuffer
// extends that to ~140, and a zoom transition keeps the old layer alive while
// the new one loads. A hard refresh or fast pan can therefore emit a few
// hundred requests within a second or two from one legitimate browser.
const (
	DefaultRateLimitRPS        = 20
	DefaultRateLimitBurst      = 400
	DefaultRateLimitTTL        = 10 * time.Minute
	DefaultRateLimitMaxEntries = 20000

	// rateLimitRetryAfterFloor keeps Retry-After at a whole second minimum,
	// since the header has one-second resolution.
	rateLimitRetryAfterFloor = time.Second
)

// RateLimitConfig configures IPRateLimiter.
type RateLimitConfig struct {
	// Logger for rejections (default: slog.Default()).
	Logger *slog.Logger
	// Proxy decides whether forwarding headers are trusted.
	Proxy ProxyPolicy
	// RPS is the sustained per-client request rate. Zero or less disables
	// the limiter entirely, making the middleware a passthrough.
	RPS float64
	// TTL is how long an idle entry is retained (default: 10m).
	TTL time.Duration
	// Burst is the bucket depth.
	Burst int
	// MaxEntries caps tracked clients (default: 20000).
	MaxEntries int
}

type bucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// IPRateLimiter is a per-client token-bucket limiter with bounded memory.
//
// Entries are evicted on an idle TTL and hard-capped, so the limiter cannot
// become the same unbounded-map problem it is meant to help contain: without
// eviction, an address-spraying attacker would grow it without limit.
type IPRateLimiter struct {
	buckets map[netip.Addr]*bucket

	// overflow is the shared fallback once MaxEntries is reached. Degrading
	// to one shared bucket is better than either unbounded growth or
	// rejecting every new client outright.
	overflow *rate.Limiter

	// now is injectable so tests can drive the bucket deterministically
	// instead of sleeping.
	now func() time.Time

	stop chan struct{}

	cfg RateLimitConfig

	mu       sync.Mutex
	stopOnce sync.Once
}

// NewIPRateLimiter creates a limiter and starts its eviction janitor. Call
// Close to stop the janitor.
func NewIPRateLimiter(cfg RateLimitConfig) *IPRateLimiter {
	if cfg.Burst <= 0 {
		cfg.Burst = DefaultRateLimitBurst
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultRateLimitTTL
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = DefaultRateLimitMaxEntries
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	l := &IPRateLimiter{
		cfg:      cfg,
		buckets:  make(map[netip.Addr]*bucket),
		overflow: rate.NewLimiter(rate.Limit(cfg.RPS), cfg.Burst),
		now:      time.Now,
		stop:     make(chan struct{}),
	}

	if l.enabled() {
		go l.janitor()
	}

	return l
}

func (l *IPRateLimiter) enabled() bool { return l.cfg.RPS > 0 }

// Allow reports whether a request from key may proceed, and if not, how long
// the caller should wait.
func (l *IPRateLimiter) Allow(key netip.Addr) (bool, time.Duration) {
	if !l.enabled() {
		return true, 0
	}

	now := l.now()
	limiter := l.limiterFor(key, now)

	if limiter.AllowN(now, 1) {
		return true, 0
	}

	// Reserve purely to read the delay, then cancel so it does not consume
	// a token the caller never gets to use.
	reservation := limiter.ReserveN(now, 1)
	delay := reservation.DelayFrom(now)
	reservation.CancelAt(now)

	if delay < rateLimitRetryAfterFloor {
		delay = rateLimitRetryAfterFloor
	}
	return false, delay
}

func (l *IPRateLimiter) limiterFor(key netip.Addr, now time.Time) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	if b, ok := l.buckets[key]; ok {
		b.lastSeen = now
		return b.limiter
	}

	if len(l.buckets) >= l.cfg.MaxEntries {
		// Try to make room before falling back to the shared bucket.
		l.sweepLocked(now)
		if len(l.buckets) >= l.cfg.MaxEntries {
			return l.overflow
		}
	}

	b := &bucket{
		limiter:  rate.NewLimiter(rate.Limit(l.cfg.RPS), l.cfg.Burst),
		lastSeen: now,
	}
	l.buckets[key] = b
	return b.limiter
}

// Middleware rejects requests from clients over their limit.
//
// It passes the ResponseWriter through untouched. Wrapping it without an
// Unwrap() http.ResponseWriter method would break http.ResponseController for
// downstream handlers, silently disabling the per-request write deadlines that
// the tile and SSE handlers rely on.
func (l *IPRateLimiter) Middleware(next http.Handler) http.Handler {
	if !l.enabled() {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := l.cfg.Proxy.ClientKey(r)
		ok, retryAfter := l.Allow(key)
		if ok {
			next.ServeHTTP(w, r)
			return
		}

		seconds := int(math.Ceil(retryAfter.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeTileError(w, "rate limit exceeded", http.StatusTooManyRequests)
	})
}

// Close stops the eviction janitor. It is idempotent.
func (l *IPRateLimiter) Close() {
	l.stopOnce.Do(func() { close(l.stop) })
}

// Len reports the number of tracked clients. Test hook.
func (l *IPRateLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

func (l *IPRateLimiter) janitor() {
	ticker := time.NewTicker(l.cfg.TTL / 2)
	defer ticker.Stop()

	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			l.sweep(l.now())
		}
	}
}

func (l *IPRateLimiter) sweep(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
}

func (l *IPRateLimiter) sweepLocked(now time.Time) {
	for key, b := range l.buckets {
		idle := now.Sub(b.lastSeen)
		if idle < l.cfg.TTL && !(idle > 0 && b.limiter.TokensAt(now) >= float64(l.cfg.Burst)) {
			continue
		}
		// Either idle past the TTL, or idle with a full bucket — in the
		// latter case the entry holds no state worth keeping, which is what
		// keeps steady-state memory near zero.
		delete(l.buckets, key)
	}
}
