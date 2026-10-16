package main

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

// The jobs of the command line are in the journal of the app, origin cli.

// journalOf points the journal at a new folder and reads it back.
func journalOf(t *testing.T) func() []jobs.Job {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir, err := jobs.DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	return func() []jobs.Job {
		list, errs := jobs.ReadJournal(dir)
		if len(errs) > 0 {
			t.Fatal(errs)
		}
		return list
	}
}

func TestImportApplyIsInTheJournal(t *testing.T) {
	read := journalOf(t)
	a, _, _ := jobApp(t, testConfig)
	if code, out, _ := run(a, "import", localFile(t, "sales.bak", "x"), "--apply", "--interval", "1ms"); code != 0 {
		t.Fatalf("code %d, out %q", code, out)
	}
	list := read()
	if len(list) != 1 || list[0].Kind != jobs.Import || list[0].Origin != jobs.OriginCLI || list[0].State != jobs.Succeeded ||
		list[0].Items[0].Name != "sales" {
		t.Fatalf("journal %+v", list)
	}
	// A dry run leaves nothing.
	read = journalOf(t)
	if code, _, _ := run(a, "import", localFile(t, "hr.bak", "x")); code != 0 || len(read()) != 0 {
		t.Fatalf("dry run: code %d, %d jobs", code, len(read()))
	}
}

func TestOperationApplyIsInTheJournal(t *testing.T) {
	read := journalOf(t)
	a, _, _ := testApp(t, testConfig)
	f := &fakeInstance{progress: []mssql.Lifecycle{mssql.Created, mssql.Success}}
	withOps(a, f, fakeObjects{"import/sales.bak": 1})
	code, out, _ := run(a, "-o", "json", "restore", "sales", "sales.bak", "--apply", "--interval", "1ms")
	list := read()
	if code != 0 || len(list) != 1 || !strings.Contains(out, `"jobId":"`+list[0].ID+`"`) {
		t.Fatalf("code %d, jobs %+v, out %s", code, list, out)
	}
	j := list[0]
	if j.Kind != jobs.Operation || j.Operation != "restore" || j.Origin != jobs.OriginCLI || j.State != jobs.Succeeded ||
		len(j.Items) != 1 || j.Items[0].Name != "sales" || len(j.Items[0].Steps) != 2 || j.Items[0].Steps[0].Kind != "pipeline.task" {
		t.Fatalf("job %+v", j)
	}
	// Every single operation.
	f = &fakeInstance{states: map[string]string{"sales": "ONLINE", "hr": "RESTORING"}, progress: []mssql.Lifecycle{mssql.Success}}
	withOps(a, f, fakeObjects{})
	for _, args := range [][]string{
		{"export", "sales", "--apply", "--interval", "1ms"},
		{"drop", "sales", "--apply", "--confirm", "sales"},
		{"finish", "hr", "--apply"},
	} {
		if code, out, errOut := run(a, args...); code != 0 {
			t.Fatalf("%v: code %d, out %s, stderr %s", args, code, out, errOut)
		}
	}
	withOps(a, &fakeInstance{progress: []mssql.Lifecycle{mssql.InProgress}}, fakeObjects{})
	if code, out, errOut := run(a, "cancel", "100", "--apply"); code != 0 {
		t.Fatalf("cancel: code %d, out %s, stderr %s", code, out, errOut)
	}
	ops := map[string]string{}
	for _, j := range read() {
		ops[j.Operation] = j.Items[0].Name + ":" + string(j.State)
	}
	want := map[string]string{"restore": "sales:succeeded", "export": "sales:succeeded", "drop": "sales:succeeded",
		"finish": "hr:succeeded", "cancel": "task 100:succeeded"}
	for op, v := range want {
		if ops[op] != v {
			t.Fatalf("journal %v, want %s %s", ops, op, v)
		}
	}
}

func TestRefusedApplyIsInTheJournal(t *testing.T) {
	read := journalOf(t)
	a, rds, _ := jobApp(t, testConfig)
	rds.states["sales"] = "ONLINE"
	if code, _, _ := run(a, "import", localFile(t, "sales.bak", "x"), "--apply"); code != exitCheckFailed {
		t.Fatalf("code %d", code)
	}
	protected := strings.Replace(testConfig, "    sql:", "    protected: true\n    sql:", 1)
	p, _, _ := jobApp(t, protected)
	if code, _, _ := run(p, "import", localFile(t, "hr.bak", "x"), "--apply", "--confirm", "no"); code != exitFailure {
		t.Fatalf("code %d", code)
	}
	var got []string
	for _, j := range read() {
		if j.State != jobs.Failed || j.Refusal == nil || j.Origin != jobs.OriginCLI || len(j.Items) != 1 || j.Items[0].State != jobs.Failed {
			t.Fatalf("job %+v", j)
		}
		got = append(got, j.Items[0].Name+":"+strconv.Itoa(j.Refusal.Status))
		if j.Refusal.Status == 412 && len(j.Refusal.Checks) == 0 {
			t.Fatalf("no checks: %+v", j.Refusal)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"hr:422", "sales:412"}) {
		t.Fatalf("refusals %v", got)
	}
}
