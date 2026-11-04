package pages

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/alajmo/mani/core/dao"
	"github.com/alajmo/mani/core/tui/misc"
	"github.com/alajmo/mani/core/tui/views"
)

type TTaskPage struct {
	focusable []*misc.TItem

	taskData *views.TTask
	pages    *tview.Pages
}

// TaskPageState is the reload-persistable state of the tasks page.
type TaskPageState struct {
	Task  views.TaskViewState
	Focus int
}

func CreateTasksPage(tasks []dao.Task) (*tview.Flex, *TTaskPage) {
	t := &TTaskPage{}

	// Data
	taskData := views.CreateTasksData(
		tasks,
		[]string{"Task", "Description", "Target", "Spec"},
		1,
		true,
		true,
		false,
	)

	// Views
	taskInfo := views.CreateTaskInfoView()

	// Pages
	taskTablePage, taskPages := t.createTaskPage(taskData)
	t.taskData = taskData
	t.pages = taskPages

	taskData.Page = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(taskTablePage, 0, 1, true).
		AddItem(taskInfo, 1, 0, false).
		AddItem(misc.Search, 1, 0, false)

	t.focusable = t.updateTaskFocusable(taskData)
	misc.TasksLastFocus = &t.focusable[0].Primitive

	// Shortcuts
	taskData.Page.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if misc.App.GetFocus() == misc.Search {
			return event
		}

		switch event.Key() {
		case tcell.KeyTab:
			nextPrimitive := misc.FocusNext(t.focusable)
			misc.TasksLastFocus = nextPrimitive
			return nil
		case tcell.KeyBacktab:
			nextPrimitive := misc.FocusPrevious(t.focusable)
			misc.TasksLastFocus = nextPrimitive
			return nil
		case tcell.KeyRune:
			if _, ok := misc.App.GetFocus().(*tview.InputField); ok {
				return event
			}
			switch event.Rune() {
			case 'C': // Clear filters
				taskData.Emitter.PublishAndWait(misc.Event{Name: "remove_task_filter", Data: ""})
				taskData.Emitter.PublishAndWait(misc.Event{Name: "remove_task_selections", Data: ""})
				taskData.Emitter.Publish(misc.Event{Name: "filter_tasks", Data: ""})
				return nil
			}
		}
		return event
	})

	return taskData.Page, t
}

// CaptureState snapshots the tasks page state before a config reload.
func (t *TTaskPage) CaptureState() TaskPageState {
	return TaskPageState{
		Task:  t.taskData.CaptureState(),
		Focus: t.focusIndex(),
	}
}

// RestoreState re-applies captured task data (without touching focus).
func (t *TTaskPage) RestoreState(state TaskPageState) {
	t.taskData.RestoreState(state.Task)
	t.pages.SwitchToPage(t.taskData.TaskStyle)
	t.focusable = t.updateTaskFocusable(t.taskData)
}

// RestoreFocus moves focus to the captured pane (clamped to existing panes).
func (t *TTaskPage) RestoreFocus(state TaskPageState) {
	if len(t.focusable) == 0 {
		return
	}

	idx := state.Focus
	if idx < 0 || idx >= len(t.focusable) {
		idx = 0
	}

	misc.App.SetFocus(t.focusable[idx].Primitive)
	misc.TasksLastFocus = &t.focusable[idx].Primitive
}

func (t *TTaskPage) TaskData() *views.TTask { return t.taskData }

func (t *TTaskPage) focusIndex() int {
	if misc.App == nil {
		return 0
	}

	current := misc.App.GetFocus()
	for i, item := range t.focusable {
		if item.Primitive == current {
			return i
		}
	}

	return 0
}

func (taskPage *TTaskPage) createTaskPage(taskData *views.TTask) (*tview.Flex, *tview.Pages) {
	isTable := taskData.TaskStyle == "task-table"

	pages := tview.NewPages().
		AddPage("task-table", tview.NewFlex().SetDirection(tview.FlexRow).AddItem(taskData.TaskTableView.Root, 0, 1, true), true, isTable).
		AddPage("task-tree", tview.NewFlex().SetDirection(tview.FlexRow).AddItem(taskData.TaskTreeView.Root, 0, 8, false), true, !isTable)

	page := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(pages, 0, 1, true)

	page.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if misc.App.GetFocus() == misc.Search {
			return event
		}

		switch event.Key() {
		case tcell.KeyCtrlE:
			if taskData.TaskStyle == "task-table" {
				taskData.TaskStyle = "task-tree"
			} else {
				taskData.TaskStyle = "task-table"
			}
			pages.SwitchToPage(taskData.TaskStyle)
			taskPage.focusable = taskPage.updateTaskFocusable(taskData)
			misc.App.SetFocus(taskPage.focusable[0].Primitive)
			misc.TasksLastFocus = &taskPage.focusable[0].Primitive
			return nil
		}
		return event
	})

	return page, pages
}

func (taskPage *TTaskPage) updateTaskFocusable(
	data *views.TTask,
) []*misc.TItem {
	focusable := []*misc.TItem{}

	if data.TaskStyle == "task-table" {
		focusable = append(
			focusable, misc.GetTUIItem(
				data.TaskTableView.Table,
				data.TaskTableView.Table.Box,
			))
	} else {
		focusable = append(
			focusable,
			misc.GetTUIItem(
				data.TaskTreeView.Tree,
				data.TaskTreeView.Tree.Box,
			))
	}

	return focusable
}
