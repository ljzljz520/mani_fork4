package tui

import (
	"github.com/alajmo/mani/core"
	"github.com/alajmo/mani/core/dao"
	"github.com/alajmo/mani/core/tui/components"
	"github.com/alajmo/mani/core/tui/misc"
	"github.com/alajmo/mani/core/tui/pages"
	"github.com/rivo/tview"
)

// reloadErrorModalName is reused so a newer error replaces the previous one
// and a successful reload discards it together with the old page tree.
const reloadErrorModalName = "reload-error-modal"

// activePages references the controllers of the currently displayed page
// tree. It is replaced atomically (on the UI goroutine) on every successful
// reload.
var activePages *pageControllers

// requestReload starts a background config reload. Calls are coalesced: at
// most one parse is in flight, and rapid bursts (atomic editor saves, quick
// successive edits) result in a single follow-up parse. The UI is only ever
// touched on the tview event queue, and a failed parse leaves the last valid
// config and interface completely intact.
func (app *App) requestReload() {
	app.reloadMu.Lock()
	if app.reloading {
		app.reloadPending = true
		app.reloadMu.Unlock()
		return
	}
	app.reloading = true
	app.reloadMu.Unlock()

	go app.runReload()
}

// runReload fully parses the main config and its import closure off the UI
// goroutine, then hands the result to the UI goroutine for an atomic swap.
func (app *App) runReload() {
	config, err := app.readConfig()

	app.enqueue(func() {
		if err != nil {
			// Keep the previous config and page tree untouched; surface the
			// located parse error instead.
			showReloadError(err)
		} else {
			app.applyConfig(&config)
		}

		app.reloadMu.Lock()
		if app.reloadPending {
			// One or more files changed while this reload was in flight:
			// parse the newest version next.
			app.reloadPending = false
			app.reloadMu.Unlock()
			go app.runReload()
		} else {
			app.reloading = false
			app.reloadMu.Unlock()
		}
	})
}

// applyConfig performs the atomic switch on the UI goroutine:
//  1. capture state keyed by stable identities,
//  2. swap the global config snapshot and theme,
//  3. rebuild the whole page tree from the new snapshot,
//  4. re-apply selections, filters, focus and command input that still
//     resolve,
//  5. publish the new root in one event-queue step and sync watched files.
func (app *App) applyConfig(config *dao.Config) {
	// Capture before anything is replaced.
	state := captureUIState()

	// Keep the original theme selection; fall back to the default theme if
	// the new config no longer defines it instead of tearing down the app.
	themeName := *misc.ThemeName
	theme, err := config.GetTheme(themeName)
	if err != nil {
		themeName = dao.DEFAULT_THEME.Name
		theme, err = config.GetTheme(themeName)
		core.CheckIfError(err)
	}

	// Swap the global snapshot. Running executions keep their own value copy
	// taken at start time (exec.Exec.Config), so they never observe the new
	// version mid-run.
	misc.Config = config
	misc.ThemeName = &themeName
	misc.LoadStyles(&theme.TUI)
	misc.TUITheme = &theme.TUI
	misc.BlockTheme = &theme.Block
	setupStyles()

	dao.ParseTasksEnv(config.TaskList)

	// Build the replacement tree from the new snapshot.
	newPages, ctrls := createPages(config.ProjectList, config.GetTags(), config.GetProjectPaths(), config.TaskList)

	// Re-apply state that still resolves against the new config.
	restoreUIState(state, ctrls)

	// Publish in one step.
	activePages = ctrls
	misc.Pages = newPages
	app.App.SetRoot(newPages, true)

	// Register global input handlers against the new widgets (SetInputCapture
	// replaces the previous closure; the old tree is dropped).
	HandleInput(app)

	// Sync the watched file set with the new import closure.
	if app.watcher != nil {
		app.watcher.SetFiles(append([]string{config.Path}, config.ConfigPaths...))
	}
}

// uiState is the full interface state carried across a reload.
type uiState struct {
	activePage    string
	search        string
	searchFocused bool

	run      pages.RunPageState
	exec     pages.ExecPageState
	projects pages.ProjectPageState
	tasks    pages.TaskPageState
}

func captureUIState() uiState {
	state := uiState{activePage: "run"}
	if activePages == nil || misc.MainPage == nil {
		return state
	}

	if name, page := misc.MainPage.GetFrontPage(); page != nil && name != "" {
		state.activePage = name
	}

	state.run = activePages.run.CaptureState()
	state.exec = activePages.exec.CaptureState()
	state.projects = activePages.projects.CaptureState()
	state.tasks = activePages.tasks.CaptureState()

	if misc.Search != nil {
		state.search = misc.Search.GetText()
		state.searchFocused = misc.App != nil && misc.App.GetFocus() == misc.Search
	}

	return state
}

func restoreUIState(state uiState, ctrls *pageControllers) {
	// Restore every page, including hidden ones.
	ctrls.run.RestoreState(state.run)
	ctrls.exec.RestoreState(state.exec)
	ctrls.projects.RestoreState(state.projects)
	ctrls.tasks.RestoreState(state.tasks)

	// Switch to the previously active page and move focus to its pane.
	SwitchToPage(state.activePage)
	switch state.activePage {
	case "run":
		ctrls.run.RestoreFocus(state.run)
	case "exec":
		ctrls.exec.RestoreFocus(state.exec)
	case "projects":
		ctrls.projects.RestoreFocus(state.projects)
	case "tasks":
		ctrls.tasks.RestoreFocus(state.tasks)
	}

	// Preserve search input/focus.
	if state.search != "" {
		misc.Search.SetText(state.search)
		if state.searchFocused {
			misc.Search.SetLabel(misc.Colorize("Search:", *misc.TUITheme.SearchLabel))
			misc.App.SetFocus(misc.Search)
		}
	}
}

// showReloadError displays a modal with the located parse error while
// leaving the last valid config and interface untouched. The error message
// contains ANSI colors, which are translated to tview tags via ANSIWriter.
func showReloadError(err error) {
	textView := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(true).
		SetTextAlign(tview.AlignLeft)
	textView.SetBackgroundColor(misc.STYLE_DEFAULT.Bg)
	textView.SetTextColor(misc.STYLE_DEFAULT.Fg)

	if _, writeErr := tview.ANSIWriter(textView).Write([]byte(err.Error())); writeErr != nil {
		textView.SetText(err.Error())
	}

	hint := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft)
	hint.SetBackgroundColor(misc.STYLE_DEFAULT.Bg)
	hint.SetText("Press escape to dismiss and continue using the last valid config.")

	content := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(textView, 0, 1, true).
		AddItem(hint, 1, 0, false)
	content.SetBorder(true).
		SetBorderPadding(1, 1, 2, 2).
		SetBorderColor(misc.STYLE_BORDER_FOCUS.Fg).
		SetBackgroundColor(misc.STYLE_DEFAULT.Bg)

	// Replace any previous reload error modal.
	misc.Pages.RemovePage(reloadErrorModalName)

	components.OpenModal(reloadErrorModalName, "Config Reload Failed", content, 80, 20)
}
