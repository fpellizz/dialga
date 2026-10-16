// Package core holds what every operation that changes something shares:
// the plan of actions the user previews first (dry-run, the default) and
// then executes, and the events of the execution.
//
// An operation never touches the instance or S3 directly: it builds a Plan
// whose actions carry the exact T-SQL or AWS call they will run. The preview
// shows those statements; Execute runs them in order, after the confirmation
// the plan asks for. It is the model of sql_apply in mssql-import.sh.
package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Kind is where an action runs.
type Kind string

const (
	KindSQL Kind = "sql" // T-SQL on the instance
	KindAWS Kind = "aws" // an AWS API call (S3, RDS)
)

// Risk says how much an action changes.
type Risk string

const (
	// Change creates or modifies something (a restore, an upload).
	Change Risk = "change"
	// Destructive removes something that cannot be brought back (a drop).
	Destructive Risk = "destructive"
)

// Action is one step of a plan.
type Action struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Kind  Kind   `json:"kind"`
	Risk  Risk   `json:"risk"`
	// Statement is the T-SQL, or the AWS call, the action runs as shown.
	Statement string `json:"statement"`
	// Run executes the action and returns a detail line for the report.
	Run func(context.Context) (string, error) `json:"-"`
}

// Plan is an operation on a target, ready to preview or execute.
type Plan struct {
	Operation string `json:"operation"`
	Target    string `json:"target"`
	Instance  string `json:"instance"`
	// Protected targets ask to retype the target name before any change.
	Protected bool `json:"protected"`
	// Subject is what a destructive plan removes (the database name): the
	// user retypes it to confirm.
	Subject string   `json:"subject,omitempty"`
	Actions []Action `json:"actions"`
}

// Confirmation is the text the user must retype before Execute, or "" when
// none is needed: the target name on a protected target, otherwise the
// subject of a destructive plan.
func (p Plan) Confirmation() string {
	switch {
	case len(p.Actions) == 0:
		return ""
	case p.Protected:
		return p.Target
	case p.destructive():
		return p.Subject
	default:
		return ""
	}
}

func (p Plan) destructive() bool {
	for _, a := range p.Actions {
		if a.Risk == Destructive {
			return true
		}
	}
	return false
}

// Preview writes the plan as the CLI shows it in dry-run.
func (p Plan) Preview(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Dry run: %s on %s (instance %s). Nothing is changed.\n", p.Operation, p.Target, p.Instance)
	for i, a := range p.Actions {
		tag := ""
		if a.Risk == Destructive {
			tag = "  [destructive]"
		}
		fmt.Fprintf(&b, "  %d. %s%s\n", i+1, a.Title, tag)
		for _, line := range strings.Split(a.Statement, "\n") {
			fmt.Fprintf(&b, "       %s\n", line)
		}
	}
	if c := p.Confirmation(); c != "" {
		fmt.Fprintf(&b, "Running it asks to retype %q.\n", c)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Status of an action during Execute.
type Status string

const (
	Running Status = "running"
	OK      Status = "ok"
	Failed  Status = "failed"
	Skipped Status = "skipped"
)

// Event reports the state of one action. The fields match check.Event, so
// the GUI draws both with the same component.
type Event struct {
	Step   string `json:"step"`
	Label  string `json:"label"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// ErrNotConfirmed means the confirmation text did not match.
var ErrNotConfirmed = errors.New("not confirmed: the text does not match")

// Execute runs the actions in order after checking the confirmation
// (confirmed must equal Confirmation()). The first failure stops the plan:
// the remaining actions are reported as skipped.
func (p Plan) Execute(ctx context.Context, confirmed string, emit func(Event)) error {
	if want := p.Confirmation(); want != "" && confirmed != want {
		return fmt.Errorf("%w (expected %q)", ErrNotConfirmed, want)
	}
	for i, a := range p.Actions {
		emit(Event{Step: a.ID, Label: a.Title, Status: Running})
		err := ctx.Err()
		detail := ""
		if err == nil && a.Run == nil {
			err = errors.New("this action has no runner")
		}
		if err == nil {
			detail, err = a.Run(ctx)
		}
		if err != nil {
			emit(Event{Step: a.ID, Label: a.Title, Status: Failed, Detail: err.Error()})
			for _, rest := range p.Actions[i+1:] {
				emit(Event{Step: rest.ID, Label: rest.Title, Status: Skipped, Detail: "an earlier action failed"})
			}
			return fmt.Errorf("%s: %w", a.Title, err)
		}
		emit(Event{Step: a.ID, Label: a.Title, Status: OK, Detail: detail})
	}
	return nil
}
