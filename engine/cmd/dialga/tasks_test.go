package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

// fakeTasks serves a fixed list and answers Get with the scripted tasks in turn.
type fakeTasks struct {
	list   []mssql.Task
	gets   []mssql.Task
	calls  int
	filter mssql.TaskFilter
}

func (f *fakeTasks) List(_ context.Context, filter mssql.TaskFilter) ([]mssql.Task, error) {
	f.filter = filter
	return f.list, nil
}

func (f *fakeTasks) Get(_ context.Context, id int) (mssql.Task, error) {
	t := f.gets[min(f.calls, len(f.gets)-1)]
	f.calls++
	t.ID = id
	return t, nil
}

// withTasks makes a read the native tasks from f; opened reports whether a
// session was opened and with which password.
func withTasks(a *app, f *fakeTasks) (opened *bool, password *string) {
	opened, password = new(bool), new(string)
	a.openTasks = func(_ context.Context, _ config.Target, pw string) (mssql.TaskReader, func(), error) {
		*opened, *password = true, pw
		return f, func() {}, nil
	}
	return opened, password
}

var sampleTasks = []mssql.Task{
	{ID: 12, Type: "RESTORE_DB", Database: "sales_copy", Percent: 100, Lifecycle: mssql.Success,
		Info:      "[2026-10-15 13:02:11.000] Task execution has started.\r\n[2026-10-15 13:05:40.000] Done.",
		CreatedAt: "2026-10-15T13:02:11", UpdatedAt: "2026-10-15T13:05:40"},
	{ID: 11, Type: "BACKUP_DB", Database: "sales", Percent: 45, Lifecycle: mssql.InProgress,
		CreatedAt: "2026-10-15T12:58:00", UpdatedAt: "2026-10-15T13:00:00"},
}

func TestStatusText(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	f := &fakeTasks{list: sampleTasks}
	_, password := withTasks(a, f)
	code, out, _ := run(a, "status")
	if code != 0 || *password != "s3cret" || f.filter.Limit != 20 || f.filter.Database != "" {
		t.Fatalf("code %d, password %q, filter %+v", code, *password, f.filter)
	}
	for _, want := range []string{"on dev-sql (instance dev-sqlserver)", "RESTORE_DB", "sales_copy",
		"2026-10-15 13:02", "IN_PROGRESS", "dialga wait <task-id>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestStatusJSONWithFilter(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	f := &fakeTasks{list: sampleTasks[:1]}
	withTasks(a, f)
	code, out, _ := run(a, "status", "dev-sql", "--db", "sales_copy", "--limit", "5", "-o", "json")
	var doc struct {
		Target string       `json:"target"`
		Tasks  []mssql.Task `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || code != 0 {
		t.Fatalf("code %d, json %v:\n%s", code, err, out)
	}
	if doc.Target != "dev-sql" || len(doc.Tasks) != 1 || doc.Tasks[0].Lifecycle != mssql.Success ||
		f.filter != (mssql.TaskFilter{Database: "sales_copy", Limit: 5}) {
		t.Fatalf("doc %+v, filter %+v", doc, f.filter)
	}
}

func TestStatusEmptyAndBadDatabase(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	withTasks(a, &fakeTasks{list: []mssql.Task{}})
	if code, out, _ := run(a, "status"); code != 0 || !strings.Contains(out, "No native backup/restore tasks on dev-sql") {
		t.Fatalf("empty: code %d, out %q", code, out)
	}
	bad, _, _ := testApp(t, testConfig)
	opened, _ := withTasks(bad, &fakeTasks{})
	if code, _, errOut := run(bad, "status", "--db", "x;drop"); code != exitFailure || *opened || !strings.Contains(errOut, "invalid database name") {
		t.Fatalf("bad db: code %d, opened %v, stderr %q", code, *opened, errOut)
	}
}

func TestWaitSuccess(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	f := &fakeTasks{gets: []mssql.Task{
		{Lifecycle: mssql.Created}, {Lifecycle: mssql.InProgress, Percent: 50}, sampleTasks[0],
	}}
	withTasks(a, f)
	code, out, _ := run(a, "wait", "12", "--interval", "1ms")
	if code != 0 || f.calls != 3 {
		t.Fatalf("code %d after %d reads:\n%s", code, f.calls, out)
	}
	for _, want := range []string{"Following task 12 on dev-sql", "task 12: CREATED 0%", "task 12: IN_PROGRESS 50%",
		"Task 12 completed: RESTORE_DB of sales_copy.", "    [2026-10-15 13:05:40.000] Done."} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestWaitFailedTaskExits3(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	withTasks(a, &fakeTasks{gets: []mssql.Task{{Type: "RESTORE_DB", Database: "x", Lifecycle: mssql.Error, Info: "boom"}}})
	code, out, _ := run(a, "wait", "7", "-o", "json")
	var doc struct {
		Task mssql.Task `json:"task"`
	}
	if code != exitTaskFailed || json.Unmarshal([]byte(out), &doc) != nil || doc.Task.ID != 7 || doc.Task.Info != "boom" {
		t.Fatalf("code %d, out %q", code, out)
	}
}

func TestWaitBadArguments(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	opened, _ := withTasks(a, &fakeTasks{})
	for _, args := range [][]string{{"wait", "abc"}, {"wait", "0"}, {"wait", "5", "--interval", "0s"}} {
		if code, _, _ := run(a, args...); code != exitFailure || *opened {
			t.Fatalf("%v: code %d, opened %v", args, code, *opened)
		}
	}
	tunnel, _, _ := testApp(t, strings.Replace(testConfig, "mode: external", "mode: tunnel", 1))
	withTasks(tunnel, &fakeTasks{})
	if code, _, errOut := run(tunnel, "wait", "5"); code != exitFailure || !strings.Contains(errOut, "v0.4") {
		t.Fatalf("tunnel: code %d, stderr %q", code, errOut)
	}
}

func TestOneLineAndShortTime(t *testing.T) {
	if got := oneLine("a\r\n  b\tc", 10); got != "a b c" {
		t.Fatalf("oneLine = %q", got)
	}
	if got := oneLine(strings.Repeat("x", 12), 5); got != "xxxx…" {
		t.Fatalf("oneLine cut = %q", got)
	}
	if got := shortTime("2026-10-15T13:02:11"); got != "2026-10-15 13:02" {
		t.Fatalf("shortTime = %q", got)
	}
}
