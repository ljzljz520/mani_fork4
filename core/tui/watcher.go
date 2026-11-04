package tui

import (
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// reloadDebounce is the quiet period after the last file event before a
// reload is triggered. A burst of events (atomic editor save: write temp +
// rename, or rapid successive edits) collapses into a single reload.
var reloadDebounce = 300 * time.Millisecond

// notifier is the subset of fsnotify.Watcher used by configWatcher, extracted
// so the coalescing logic can be tested with a fake backend.
type notifier interface {
	Add(path string) error
	Remove(path string) error
	Close() error
	Events() <-chan fsnotify.Event
	Errors() <-chan error
}

type fsnotifyBackend struct{ w *fsnotify.Watcher }

func (b *fsnotifyBackend) Add(path string) error         { return b.w.Add(path) }
func (b *fsnotifyBackend) Remove(path string) error      { return b.w.Remove(path) }
func (b *fsnotifyBackend) Close() error                  { return b.w.Close() }
func (b *fsnotifyBackend) Events() <-chan fsnotify.Event { return b.w.Events }
func (b *fsnotifyBackend) Errors() <-chan error          { return b.w.Errors }

// configWatcher watches the directories containing the main config and every
// imported file and triggers a coalesced reload whenever one of them changes
// (including atomic rename-in saves). The watched set is refreshed after a
// successful reload to follow added/removed imports.
type configWatcher struct {
	onChange func()
	backend  notifier

	mu    sync.Mutex
	files map[string]struct{}
	dirs  map[string]struct{}

	stop chan struct{}
	done chan struct{}
}

func WatchFiles(app *App, files ...string) *configWatcher {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil
	}

	cw := newConfigWatcher(app.requestReload, &fsnotifyBackend{w}, files...)
	go cw.watch()

	return cw
}

func newConfigWatcher(onChange func(), backend notifier, files ...string) *configWatcher {
	cw := &configWatcher{
		onChange: onChange,
		backend:  backend,
		files:    map[string]struct{}{},
		dirs:     map[string]struct{}{},
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	cw.SetFiles(files)

	return cw
}

func (c *configWatcher) Stop() {
	select {
	case <-c.stop:
		return
	default:
		close(c.stop)
	}

	<-c.done
	_ = c.backend.Close()
}

// SetFiles atomically replaces the set of files whose changes trigger a
// reload (the main config plus the current import closure). Watchers for
// directories that are no longer needed are removed; new directories are
// added. Safe to call from the UI goroutine while the watcher goroutine runs.
func (c *configWatcher) SetFiles(files []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	wantedFiles := make(map[string]struct{}, len(files))
	wantedDirs := make(map[string]struct{}, len(files))
	for _, f := range files {
		if f == "" {
			continue
		}
		abs, err := filepath.Abs(f)
		if err != nil {
			continue
		}
		wantedFiles[abs] = struct{}{}
		wantedDirs[filepath.Dir(abs)] = struct{}{}
	}

	for d := range wantedDirs {
		if _, ok := c.dirs[d]; ok {
			continue
		}
		if err := c.backend.Add(d); err == nil {
			c.dirs[d] = struct{}{}
		}
	}

	for d := range c.dirs {
		if _, ok := wantedDirs[d]; ok {
			continue
		}
		_ = c.backend.Remove(d)
		delete(c.dirs, d)
	}

	c.files = wantedFiles
}

func (c *configWatcher) isWatchedFile(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.files[name]
	return ok
}

func (c *configWatcher) watch() {
	defer close(c.done)

	var timer *time.Timer
	var timerCh <-chan time.Time

	arm := func() {
		if timer == nil {
			timer = time.NewTimer(reloadDebounce)
			timerCh = timer.C
			return
		}

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(reloadDebounce)
	}

	for {
		select {
		case <-c.stop:
			return
		case e, ok := <-c.backend.Events():
			if !ok {
				return
			}
			// Atomic editor saves emit rename-out/rename-in (CREATE) for the
			// same path; any op on a watched file rearms the single timer.
			if c.isWatchedFile(e.Name) {
				arm()
			}
		case err, ok := <-c.backend.Errors():
			if !ok {
				return
			}
			// A watcher error must not tear down the running TUI; the last
			// valid config and interface stay in place.
			_ = err
		case <-timerCh:
			timer = nil
			timerCh = nil
			c.onChange()
		}
	}
}
