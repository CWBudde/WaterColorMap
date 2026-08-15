package pipeline

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/tilestamp"
)

// fakeStampStore is an in-memory StampStore. getErr makes the lookup fail, which
// is the "unreadable stamp" case.
type fakeStampStore struct {
	stamps map[string]tilestamp.Stamp
	getErr error
	puts   []tilestamp.Stamp
}

func newFakeStampStore() *fakeStampStore {
	return &fakeStampStore{stamps: map[string]tilestamp.Stamp{}}
}

func stampKey(z, x, y int, suffix string) string {
	return tile.NewCoords(uint32(z), uint32(x), uint32(y)).String() + suffix
}

func (f *fakeStampStore) Put(s tilestamp.Stamp) error {
	f.puts = append(f.puts, s)
	f.stamps[stampKey(s.Z, s.X, s.Y, s.Suffix)] = s
	return nil
}

func (f *fakeStampStore) Get(z, x, y int, suffix string) (tilestamp.Stamp, bool, error) {
	if f.getErr != nil {
		return tilestamp.Stamp{}, false, f.getErr
	}
	s, ok := f.stamps[stampKey(z, x, y, suffix)]
	return s, ok, nil
}

// freshnessGenerator builds a folder-backed generator with the given policy and
// store, and creates the tile file so only freshness is left to decide.
func freshnessGenerator(t *testing.T, store StampStore, policy FreshnessPolicy) (*Generator, tile.Coords, string) {
	t.Helper()

	coords := tile.Coords{Z: 13, X: 100, Y: 200}
	finalPath := filepath.Join(t.TempDir(), "z13_x100_y200.png")
	if err := os.WriteFile(finalPath, []byte("existing tile"), 0o600); err != nil {
		t.Fatalf("write tile: %v", err)
	}

	g := &Generator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		options: GeneratorOptions{
			StampStore:  store,
			Freshness:   policy,
			RendererRev: "v2+cafe",
		},
	}
	return g, coords, finalPath
}

func TestTileExistsFreshness(t *testing.T) {
	lastImport := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	before := lastImport.Add(-24 * time.Hour)
	after := lastImport.Add(24 * time.Hour)

	fresh := tilestamp.Stamp{
		Z: 13, X: 100, Y: 200,
		OSMBase: after, RenderedAt: after, RendererRev: "v2+cafe",
	}

	tests := []struct {
		name   string
		stamp  *tilestamp.Stamp
		policy FreshnessPolicy
		want   bool
	}{
		{
			// The property the whole feature rests on: without any policy the
			// stamp store is never consulted and an existing tile is skipped,
			// exactly as before.
			name:   "no policy skips an existing tile",
			stamp:  nil,
			policy: FreshnessPolicy{},
			want:   true,
		},
		{
			name:   "fresh data stamp skips",
			stamp:  &fresh,
			policy: FreshnessPolicy{DataBefore: lastImport},
			want:   true,
		},
		{
			name: "stale data stamp re-renders",
			stamp: &tilestamp.Stamp{
				Z: 13, X: 100, Y: 200, OSMBase: before, RenderedAt: after,
			},
			policy: FreshnessPolicy{DataBefore: lastImport},
			want:   false,
		},
		{
			// Unknown is not fresh: nothing recorded the data version, so the
			// only answer that cannot leave stale tiles behind is "render".
			name: "stamp without a data timestamp re-renders",
			stamp: &tilestamp.Stamp{
				Z: 13, X: 100, Y: 200, RenderedAt: after,
			},
			policy: FreshnessPolicy{DataBefore: lastImport},
			want:   false,
		},
		{
			name:   "missing stamp re-renders",
			stamp:  nil,
			policy: FreshnessPolicy{DataBefore: lastImport},
			want:   false,
		},
		{
			name: "old render time re-renders",
			stamp: &tilestamp.Stamp{
				Z: 13, X: 100, Y: 200, OSMBase: after, RenderedAt: before,
			},
			policy: FreshnessPolicy{RenderedBefore: lastImport},
			want:   false,
		},
		{
			name:   "recent render time skips",
			stamp:  &fresh,
			policy: FreshnessPolicy{RenderedBefore: lastImport},
			want:   true,
		},
		{
			name: "different renderer revision re-renders",
			stamp: &tilestamp.Stamp{
				Z: 13, X: 100, Y: 200, RenderedAt: after, RendererRev: "v1+beef",
			},
			policy: FreshnessPolicy{RendererRev: true},
			want:   false,
		},
		{
			name:   "same renderer revision skips",
			stamp:  &fresh,
			policy: FreshnessPolicy{RendererRev: true},
			want:   true,
		},
		{
			// Any one failing criterion is enough to re-render.
			name:   "fresh data but a different renderer re-renders",
			stamp:  &tilestamp.Stamp{Z: 13, X: 100, Y: 200, OSMBase: after, RenderedAt: after},
			policy: FreshnessPolicy{DataBefore: lastImport, RendererRev: true},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStampStore()
			if tt.stamp != nil {
				if err := store.Put(*tt.stamp); err != nil {
					t.Fatalf("Put: %v", err)
				}
			}

			g, coords, finalPath := freshnessGenerator(t, store, tt.policy)
			if got := g.tileExists(coords, finalPath, ""); got != tt.want {
				t.Errorf("tileExists = %v, want %v", got, tt.want)
			}
		})
	}
}

// A nil store must leave behaviour untouched — that is what keeps every
// existing caller and test fake working — but it cannot be read as "fresh"
// either once a policy is asking questions.
func TestTileExistsWithoutStampStore(t *testing.T) {
	t.Run("no policy", func(t *testing.T) {
		g, coords, finalPath := freshnessGenerator(t, nil, FreshnessPolicy{})
		if !g.tileExists(coords, finalPath, "") {
			t.Error("tileExists = false, want true: a nil store must not change the unconfigured path")
		}
	})

	t.Run("policy set", func(t *testing.T) {
		g, coords, finalPath := freshnessGenerator(t, nil, FreshnessPolicy{RendererRev: true})
		if g.tileExists(coords, finalPath, "") {
			t.Error("tileExists = true, want false: no store means the question is unanswerable")
		}
	})
}

func TestTileExistsWithUnreadableStamp(t *testing.T) {
	store := newFakeStampStore()
	store.getErr = errors.New("database is locked")

	g, coords, finalPath := freshnessGenerator(t, store, FreshnessPolicy{RendererRev: true})
	if g.tileExists(coords, finalPath, "") {
		t.Error("tileExists = true, want false: a failed stamp lookup must render")
	}
}

// A stale tile is only re-rendered if it is found in the first place; freshness
// must never resurrect a tile that is not there.
func TestFreshStampDoesNotInventAMissingTile(t *testing.T) {
	store := newFakeStampStore()
	if err := store.Put(tilestamp.Stamp{
		Z: 13, X: 100, Y: 200, OSMBase: time.Now(), RenderedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	g := &Generator{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		options: GeneratorOptions{StampStore: store, Freshness: FreshnessPolicy{DataBefore: time.Now()}},
	}

	missing := filepath.Join(t.TempDir(), "z13_x100_y200.png")
	if g.tileExists(tile.Coords{Z: 13, X: 100, Y: 200}, missing, "") {
		t.Error("tileExists = true for a tile that is not on disk")
	}
}

// The stamp must carry the fetched data's provenance, the running renderer's
// revision, and the tile's own suffix.
func TestPutStamp(t *testing.T) {
	store := newFakeStampStore()
	osmBase := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	g := &Generator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		options: GeneratorOptions{
			StampStore:  store,
			RendererRev: "v2+cafe",
		},
	}

	g.putStamp(tile.Coords{Z: 9, X: 4, Y: 7}, "@2x", &renderLayersResult{
		dataTimestamp: osmBase,
		dataSource:    "http://localhost:12345/api/interpreter",
	})

	if len(store.puts) != 1 {
		t.Fatalf("recorded %d stamps, want 1", len(store.puts))
	}
	got := store.puts[0]

	if got.Z != 9 || got.X != 4 || got.Y != 7 || got.Suffix != "@2x" {
		t.Errorf("stamp addresses %d/%d/%d%q, want 9/4/7@2x", got.Z, got.X, got.Y, got.Suffix)
	}
	if !got.OSMBase.Equal(osmBase) {
		t.Errorf("OSMBase = %s, want %s", got.OSMBase, osmBase)
	}
	if got.Source != "http://localhost:12345/api/interpreter" {
		t.Errorf("Source = %q, want the endpoint that answered", got.Source)
	}
	if got.RendererRev != "v2+cafe" {
		t.Errorf("RendererRev = %q, want %q", got.RendererRev, "v2+cafe")
	}
	if got.RenderedAt.IsZero() {
		t.Error("RenderedAt is zero")
	}
}

// A stamp store that refuses the write must not fail the tile: the tile is real
// and the stamp is bookkeeping about it.
func TestPutStampFailureIsNotFatal(t *testing.T) {
	g := &Generator{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		options: GeneratorOptions{StampStore: failingStampStore{}},
	}

	// The contract is that this returns normally; a panic or a propagated error
	// would show up as a failed tile.
	g.putStamp(tile.Coords{Z: 1, X: 0, Y: 0}, "", &renderLayersResult{})
}

type failingStampStore struct{}

func (failingStampStore) Put(tilestamp.Stamp) error { return errors.New("disk full") }

func (failingStampStore) Get(int, int, int, string) (tilestamp.Stamp, bool, error) {
	return tilestamp.Stamp{}, false, errors.New("disk full")
}

// A nil store must be a no-op rather than a nil dereference.
func TestPutStampWithoutStore(t *testing.T) {
	g := &Generator{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	g.putStamp(tile.Coords{Z: 1, X: 0, Y: 0}, "", &renderLayersResult{})
}
