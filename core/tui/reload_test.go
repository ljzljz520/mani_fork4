package tui

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alajmo/mani/core/dao"
	"github.com/alajmo/mani/core/tui/misc"
	"github.com/rivo/tview"
)

const validConfigV1 = `projects:
  alpha:
    path: alpha
  beta:
    path: beta
tasks:
  hello:
    cmd: echo hello
  world:
    cmd: echo world
`

const validConfigV2 = `projects:
  alpha:
    path: alpha
tasks:
  hello:
    cmd: echo hello
`

const invalidConfig = "tasks:\n  hello:\n    cmd: [unclosed\n"

func writeTestConfig(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "mani.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// newTestApp builds the full page tree headlessly. enqueue runs callbacks
// synchronously on the calling (background) goroutine; wait() blocks until
// one queued callback has settled.
func newTestApp(t *testing.T, path string) (*App, func()) {
	t.Helper()

	theme := dao.DEFAULT_THEME
	misc.LoadStyles(&theme.TUI)
	misc.TUITheme = &theme.TUI
	misc.BlockTheme = &theme.Block

	config, err := dao.ReadConfig(path, "", true)
	if err != nil {
		t.Fatalf("initial config parse failed: %v", err)
	}

	tviewApp := tview.NewApplication()
	app := &App{App: tviewApp}
	misc.App = tviewApp

	done := make(chan struct{}, 32)
	app.enqueue = func(f func()) {
		f()
		done <- struct{}{}
	}
	app.readConfig = func() (dao.Config, error) {
		return dao.ReadConfig(path, "", true)
	}

	app.setupApp(&config, "default")

	wait := func() {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("reload did not settle in time")
		}
	}

	return app, wait
}

func (app *App) reloadState() (bool, bool) {
	app.reloadMu.Lock()
	defer app.reloadMu.Unlock()
	return app.reloading, app.reloadPending
}

// A failed parse keeps the last valid config and interface untouched and
// surfaces a located error modal.
func TestReloadFailureKeepsLastValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, validConfigV1)
	app, wait := newTestApp(t, path)

	oldConfig := misc.Config
	oldRunTasks := activePages.run.TaskData()
	oldRunTasks.TasksSelected["hello"] = true

	writeTestConfig(t, dir, invalidConfig)
	app.requestReload()
	wait()

	if misc.Config != oldConfig {
		t.Fatal("failed reload must not replace the active config")
	}
	if activePages.run.TaskData() != oldRunTasks {
		t.Fatal("failed reload must not rebuild the page tree")
	}
	if !oldRunTasks.TasksSelected["hello"] {
		t.Fatal("failed reload altered existing UI state")
	}
	if name, _ := misc.Pages.GetFrontPage(); name != reloadErrorModalName {
		t.Fatalf("front page = %q, want reload error modal", name)
	}
}

// A successful reload swaps config atomically, restores selections/filters by
// stable identity, preserves the command input, removes a stale error modal,
// leaves value-copied startup snapshots untouched, and re-syncs watched files.
func TestReloadSuccessAtomicSwapAndIdentityRestore(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, validConfigV1)
	app, wait := newTestApp(t, path)

	// Wire a fake watcher to verify the watched set is synced after reload.
	backend := newFakeBackend()
	cw := newConfigWatcher(func() {}, backend)
	go cw.watch()
	t.Cleanup(cw.Stop)
	app.watcher = cw

	// User state on the old version.
	activePages.run.TaskData().TasksSelected["hello"] = true
	activePages.run.TaskData().TasksSelected["world"] = true
	activePages.run.ProjectData().ProjectsSelected["alpha"] = true
	activePages.run.ProjectData().ProjectsSelected["beta"] = true
	activePages.exec.CommandView().SetText("ls -la", true)

	// Running tasks hold a value copy of the config taken at start time
	// (mirrors exec.Exec{Config: *misc.Config}).
	startupSnapshot := *misc.Config

	// First break the config (modal opens), then publish the fixed version.
	writeTestConfig(t, dir, invalidConfig)
	app.requestReload()
	wait()
	if name, _ := misc.Pages.GetFrontPage(); name != reloadErrorModalName {
		t.Fatalf("front page = %q, want reload error modal", name)
	}

	writeTestConfig(t, dir, validConfigV2)
	app.requestReload()
	wait()

	cfg := misc.Config
	if len(cfg.TaskList) != 1 || cfg.TaskList[0].Name != "hello" {
		t.Fatalf("new config tasks = %+v, want only hello", cfg.TaskList)
	}
	if len(cfg.ProjectList) != 1 || cfg.ProjectList[0].Name != "alpha" {
		t.Fatalf("new config projects = %+v, want only alpha", cfg.ProjectList)
	}

	// The startup snapshot must keep observing the old version.
	if len(startupSnapshot.TaskList) != 2 {
		t.Fatal("running task snapshot was mutated by reload")
	}
	if len(startupSnapshot.ProjectList) != 2 {
		t.Fatal("running task snapshot projects were mutated by reload")
	}

	// Run-page task selections restored by identity.
	runTasks := activePages.run.TaskData()
	if !runTasks.TasksSelected["hello"] {
		t.Fatal("selection for still-existing task hello was not restored")
	}
	if _, ok := runTasks.TasksSelected["world"]; ok {
		t.Fatal("vanished task world must not be reintroduced")
	}

	runProjects := activePages.run.ProjectData()
	if !runProjects.ProjectsSelected["alpha"] {
		t.Fatal("selection for still-existing project alpha was not restored")
	}
	if _, ok := runProjects.ProjectsSelected["beta"]; ok {
		t.Fatal("vanished project beta must not be reintroduced")
	}

	// Command input preserved verbatim.
	if got := activePages.exec.CommandView().GetText(); got != "ls -la" {
		t.Fatalf("command input = %q, want %q", got, "ls -la")
	}

	// Error modal discarded together with the old page tree.
	if name, _ := misc.Pages.GetFrontPage(); name != "main" {
		t.Fatalf("front page = %q, want main", name)
	}

	// Watched file set synced to the new main config directory.
	added, _ := backend.snapshot()
	if !contains(added, dir) {
		t.Fatalf("watcher did not start watching config dir, added=%v", added)
	}
}

// Requests made while a parse is in flight coalesce into one follow-up
// parse, results apply in order and a stale failure can never overwrite a
// newer successful version.
func TestReloadInFlightCoalescingAndOrdering(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, validConfigV1)
	app, wait := newTestApp(t, path)

	activePages.run.TaskData().TasksSelected["hello"] = true

	gate := make(chan struct{})
	var readMu sync.Mutex
	reads := 0
	app.readConfig = func() (dao.Config, error) {
		readMu.Lock()
		i := reads
		reads++
		readMu.Unlock()

		if i == 0 {
			<-gate // first parse stays in flight
		}
		if i == 1 {
			// File fixed before the second (coalesced) parse.
			writeTestConfig(t, dir, validConfigV2)
		}
		return dao.ReadConfig(path, "", true)
	}

	writeTestConfig(t, dir, invalidConfig)
	app.requestReload()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if reloading, _ := app.reloadState(); reloading {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if reloading, _ := app.reloadState(); !reloading {
		t.Fatal("expected first reload to be in flight")
	}

	// Burst while parsing: must collapse into a single pending reload.
	app.requestReload()
	app.requestReload()
	if reloading, pending := app.reloadState(); !reloading || !pending {
		t.Fatalf("state = (reloading=%v pending=%v), want (true true)", reloading, pending)
	}

	close(gate)
	wait() // cycle 1 settles: parse failure -> error modal, schedules follow-up
	wait() // cycle 2 settles: coalesced parse success -> atomic swap

	readMu.Lock()
	gotReads := reads
	readMu.Unlock()
	if gotReads != 2 {
		t.Fatalf("parses = %d, want exactly 2 (burst coalesced)", gotReads)
	}

	if reloading, pending := app.reloadState(); reloading || pending {
		t.Fatalf("state after settle = (reloading=%v pending=%v), want (false false)", reloading, pending)
	}

	if len(misc.Config.TaskList) != 1 || misc.Config.TaskList[0].Name != "hello" {
		t.Fatal("stale failure must not win over newer successful parse")
	}
	if name, _ := misc.Pages.GetFrontPage(); name != "main" {
		t.Fatalf("front page = %q, want main after successful parse", name)
	}
	if !activePages.run.TaskData().TasksSelected["hello"] {
		t.Fatal("selection restored across failure-then-success sequence")
	}
}
