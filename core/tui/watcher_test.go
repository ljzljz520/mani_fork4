package tui

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

type fakeBackend struct {
	mu      sync.Mutex
	added   []string
	removed []string

	events chan fsnotify.Event
	errs   chan error
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		events: make(chan fsnotify.Event, 64),
		errs:   make(chan error, 8),
	}
}

func (f *fakeBackend) Add(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added = append(f.added, path)
	return nil
}

func (f *fakeBackend) Remove(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, path)
	return nil
}

func (f *fakeBackend) Close() error                  { return nil }
func (f *fakeBackend) Events() <-chan fsnotify.Event { return f.events }
func (f *fakeBackend) Errors() <-chan error          { return f.errs }

func (f *fakeBackend) snapshot() (added, removed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.added...), append([]string{}, f.removed...)
}

func newTestWatcher(t *testing.T) (*configWatcher, *fakeBackend, func() int) {
	t.Helper()

	prevDebounce := reloadDebounce
	reloadDebounce = 25 * time.Millisecond
	t.Cleanup(func() { reloadDebounce = prevDebounce })

	var count int
	var countMu sync.Mutex
	backend := newFakeBackend()
	cw := newConfigWatcher(func() {
		countMu.Lock()
		count++
		countMu.Unlock()
	}, backend)
	t.Cleanup(cw.Stop)
	go cw.watch()

	return cw, backend, func() int {
		countMu.Lock()
		defer countMu.Unlock()
		return count
	}
}

func waitReloads(t *testing.T, want int, get func() int) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if get() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d reloads, got %d", want, get())
}

// Atomic editor saves (write temp + rename over target) must result in a
// single reload, and events for unrelated files must be ignored.
func TestWatcherAtomicSaveCoalescesIntoSingleReload(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "mani.yaml")
	tmp := filepath.Join(dir, ".mani.yaml.swp")

	cw, backend, count := newTestWatcher(t)
	cw.SetFiles([]string{main})

	// Typical atomic save sequence: temp write, rename out, rename in.
	backend.events <- fsnotify.Event{Name: tmp, Op: fsnotify.Create}
	backend.events <- fsnotify.Event{Name: tmp, Op: fsnotify.Write}
	backend.events <- fsnotify.Event{Name: main, Op: fsnotify.Rename}
	backend.events <- fsnotify.Event{Name: main, Op: fsnotify.Create}
	backend.events <- fsnotify.Event{Name: main, Op: fsnotify.Chmod}

	waitReloads(t, 1, count)

	// Allow any spuriously queued timer to fire.
	time.Sleep(60 * time.Millisecond)
	if got := count(); got != 1 {
		t.Fatalf("atomic save triggered %d reloads, want 1", got)
	}
}

// Rapid successive modifications collapse into a single reload; a later
// isolated change is delivered as a separate reload.
func TestWatcherRapidChangesCoalesce(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "mani.yaml")

	cw, backend, count := newTestWatcher(t)
	cw.SetFiles([]string{main})

	for range 10 {
		backend.events <- fsnotify.Event{Name: main, Op: fsnotify.Write}
		time.Sleep(3 * time.Millisecond)
	}

	waitReloads(t, 1, count)
	time.Sleep(60 * time.Millisecond)
	if got := count(); got != 1 {
		t.Fatalf("rapid changes triggered %d reloads, want 1", got)
	}

	backend.events <- fsnotify.Event{Name: main, Op: fsnotify.Write}
	waitReloads(t, 2, count)
}

// Adding/removing imports updates the watched directory set, and events for
// files no longer in the set stop triggering reloads.
func TestWatcherSetFilesSyncsImportClosure(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	main := filepath.Join(dir1, "mani.yaml")
	imp := filepath.Join(dir2, "tasks.yaml")

	cw, backend, count := newTestWatcher(t)
	cw.SetFiles([]string{main, imp})

	added, _ := backend.snapshot()
	if !contains(added, dir1) || !contains(added, dir2) {
		t.Fatalf("expected both import dirs watched, got %v", added)
	}

	// Import removed: its directory stops being watched and its events ignored.
	cw.SetFiles([]string{main})
	_, removed := backend.snapshot()
	if !contains(removed, dir2) {
		t.Fatalf("expected removed watch on %s, got %v", dir2, removed)
	}

	backend.events <- fsnotify.Event{Name: imp, Op: fsnotify.Write}
	time.Sleep(60 * time.Millisecond)
	if got := count(); got != 0 {
		t.Fatalf("removed import triggered %d reloads, want 0", got)
	}

	// Main config events still reload.
	backend.events <- fsnotify.Event{Name: main, Op: fsnotify.Write}
	waitReloads(t, 1, count)
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
