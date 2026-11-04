package tui

import (
	"os"
	"sync"

	"github.com/alajmo/mani/core"
	"github.com/alajmo/mani/core/dao"
	"github.com/alajmo/mani/core/tui/misc"
	"github.com/rivo/tview"
)

func RunTui(config *dao.Config, themeName string, reload bool) {
	app := NewApp(config, themeName)

	if reload {
		app.watcher = WatchFiles(app, append([]string{config.Path}, config.ConfigPaths...)...)
	}

	runErr := app.Run()

	if app.watcher != nil {
		app.watcher.Stop()
	}

	if runErr != nil {
		os.Exit(1)
	}
}

type App struct {
	App     *tview.Application
	watcher *configWatcher

	// Reload coordination. At most one reload is parsed at a time; if another
	// change arrives while one is in flight (or while its result is being
	// applied) exactly one more parse is scheduled. This coalesces bursts of
	// file events (atomic saves, rapid edits) while guaranteeing that results
	// are consumed in order on the UI goroutine, so stale versions can never
	// replace newer ones.
	reloadMu      sync.Mutex
	reloading     bool
	reloadPending bool

	// readConfig fully parses the main config and its import closure off the
	// UI goroutine. Inputs are snapshotted from the current valid config so
	// concurrent swaps cannot mix versions.
	readConfig func() (dao.Config, error)

	// enqueue runs f on the tview event queue. It is a seam for testing.
	enqueue func(f func())
}

func NewApp(config *dao.Config, themeName string) *App {
	tviewApp := tview.NewApplication()
	app := &App{App: tviewApp}

	app.readConfig = func() (dao.Config, error) {
		current := misc.Config
		userConfigPath := ""
		// Preserve the user config resolution from the initial invocation.
		if current.UserConfigFile != nil {
			userConfigPath = *current.UserConfigFile
		}
		return dao.ReadConfig(current.Path, userConfigPath, current.Color)
	}
	app.enqueue = func(f func()) {
		app.App.QueueUpdateDraw(f)
	}

	app.setupApp(config, themeName)

	return app
}

func (app *App) Run() error {
	return app.App.SetRoot(misc.Pages, true).EnableMouse(true).Run()
}

func (app *App) setupApp(config *dao.Config, themeName string) {
	misc.Config = config
	misc.ThemeName = &themeName

	theme, err := misc.Config.GetTheme(themeName)
	core.CheckIfError(err)

	misc.LoadStyles(&theme.TUI)
	misc.TUITheme = &theme.TUI
	misc.BlockTheme = &theme.Block

	// Data
	projects := config.ProjectList
	tasks := config.TaskList
	dao.ParseTasksEnv(tasks)
	projectTags := config.GetTags()
	projectPaths := config.GetProjectPaths()

	// Styles
	setupStyles()

	// Create pages
	misc.App = app.App
	newPages, ctrls := createPages(projects, projectTags, projectPaths, tasks)
	activePages = ctrls
	misc.Pages = newPages

	// Global input handling
	HandleInput(app)
}
