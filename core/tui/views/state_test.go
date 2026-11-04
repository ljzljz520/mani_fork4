package views

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alajmo/mani/core/dao"
	"github.com/alajmo/mani/core/tui/misc"
)

func initTestStyles() {
	theme := dao.DEFAULT_THEME
	misc.LoadStyles(&theme.TUI)
	misc.TUITheme = &theme.TUI
	misc.BlockTheme = &theme.Block
}

func TestTaskViewRestoreStateKeepsExistingIdentity(t *testing.T) {
	initTestStyles()

	oldTasks := []dao.Task{
		{Name: "hello", Desc: "Hello"},
		{Name: "world", Desc: "World"},
	}
	old := CreateTasksData(oldTasks, []string{"Name", "Description"}, 1, true, true, true)

	// User switched to tree, typed a filter and selected "hello".
	old.TaskStyle = "task-tree"
	*old.taskFilterValue = "hel"
	old.toggleSelectTask("hello")

	state := old.CaptureState()

	if state.TaskStyle != "task-tree" {
		t.Fatalf("captured style = %q, want task-tree", state.TaskStyle)
	}
	if state.Filter != "hel" {
		t.Fatalf("captured filter = %q, want hel", state.Filter)
	}
	if !state.Selected["hello"] || state.Selected["world"] {
		t.Fatalf("captured selections = %v, want only hello", state.Selected)
	}

	// New config: "world" disappeared, "newtask" appeared.
	newTasks := []dao.Task{
		{Name: "hello", Desc: "Hello"},
		{Name: "newtask", Desc: "New"},
	}
	newView := CreateTasksData(newTasks, []string{"Name", "Description"}, 1, true, true, true)
	newView.RestoreState(state)

	if newView.TaskStyle != "task-tree" {
		t.Fatalf("restored style = %q, want task-tree", newView.TaskStyle)
	}
	if !newView.TasksSelected["hello"] {
		t.Fatal("selection for still-existing task hello was not restored")
	}
	if newView.TasksSelected["newtask"] {
		t.Fatal("new task must default to unselected")
	}
	if len(newView.TasksFiltered) != 1 || newView.TasksFiltered[0].Name != "hello" {
		t.Fatalf("filter not re-applied: %+v", newView.TasksFiltered)
	}
	if got := newView.TaskTableView.Filter.GetText(); got != "hel" {
		t.Fatalf("filter input text = %q, want hel", got)
	}
}

func TestProjectViewRestoreStateKeepsExistingIdentities(t *testing.T) {
	initTestStyles()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "mani.yaml")
	yamlContent := `projects:
  alpha:
    path: alpha
    tags: [t1]
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	config, err := dao.ReadConfig(configPath, "", true)
	if err != nil {
		t.Fatal(err)
	}
	misc.Config = &config

	// Old view: alpha + beta, tags t1 + t2, path p1.
	oldProjects := []dao.Project{
		{Name: "alpha", Path: "alpha", Tags: []string{"t1"}},
		{Name: "beta", Path: "beta"},
	}
	old := CreateProjectsData(oldProjects, []string{"t1", "t2"}, []string{"p1"},
		[]string{"Project", "Description", "Tag"}, 2, true, true, true, true, true)

	old.ProjectStyle = "project-tree"
	*old.projectFilterValue = "alp"
	*old.projectTagFilterValue = "zzz"
	old.toggleSelectProject("alpha")
	old.ProjectTagsSelected["t1"] = true
	old.ProjectTagsSelected["t2"] = true
	old.ProjectPathsSelected["p1"] = true

	state := old.CaptureState()

	// New view built from the freshly parsed config (only alpha, tag t1, no paths).
	newView := CreateProjectsData(config.ProjectList, config.GetTags(), config.GetProjectPaths(),
		[]string{"Project", "Description", "Tag"}, 2, true, true, true, true, true)
	newView.RestoreState(state)

	if newView.ProjectStyle != "project-tree" {
		t.Fatalf("restored style = %q, want project-tree", newView.ProjectStyle)
	}
	if !newView.ProjectsSelected["alpha"] {
		t.Fatal("selection for still-existing project alpha was not restored")
	}
	if _, ok := newView.ProjectsSelected["beta"]; ok {
		t.Fatal("vanished project beta must not be reintroduced")
	}
	if !newView.ProjectTagsSelected["t1"] {
		t.Fatal("selection for still-existing tag t1 was not restored")
	}
	if newView.ProjectTagsSelected["t2"] {
		t.Fatal("vanished tag t2 must not remain selected")
	}
	if len(newView.ProjectsFiltered) != 1 || newView.ProjectsFiltered[0].Name != "alpha" {
		t.Fatalf("filters/tags not re-applied: %+v", newView.ProjectsFiltered)
	}
	if got := newView.ProjectTableView.Filter.GetText(); got != "alp" {
		t.Fatalf("project filter input = %q, want alp", got)
	}
	if got := newView.TagView.Filter.GetText(); got != "zzz" {
		t.Fatalf("tag filter input = %q, want zzz", got)
	}
}
