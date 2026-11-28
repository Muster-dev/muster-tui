package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Task is one running async operation (engine, docker or ssh call).
type Task struct {
	ID      int
	Label   string
	Owner   string // screen key, "" = app
	FG      bool   // foreground tasks are what Esc / Ctrl+C cancel
	Started time.Time
	cancel  context.CancelFunc
	ctx     context.Context
}

// Canceled reports whether the task was cancelled.
func (t *Task) Canceled() bool { return t.ctx.Err() != nil }

// Tasks registers and cancels tasks. It is only touched from Update.
type Tasks struct {
	next  int
	tasks map[int]*Task
	order []int
	now   func() time.Time
}

func newTasks(now func() time.Time) *Tasks {
	return &Tasks{tasks: map[int]*Task{}, now: now}
}

// TaskMsg wraps a message produced by a task.
type TaskMsg struct {
	ID  int
	Msg tea.Msg
}

// TaskEndedMsg is broadcast when a task finishes or is cancelled.
type TaskEndedMsg struct {
	ID       int
	Canceled bool
}

type taskDoneMsg struct {
	ID  int
	Msg tea.Msg
}

type streamOpenMsg struct {
	ID  int
	ch  <-chan tea.Msg
	err error
}

type streamItemMsg struct {
	ID  int
	Msg tea.Msg
	ch  <-chan tea.Msg
}

type streamEndMsg struct{ ID int }

// taskErrMsg reports a stream that failed to start.
type taskErrMsg struct{ Err error }

func (t *Tasks) add(label, owner string, fg bool) *Task {
	t.next++
	ctx, cancel := context.WithCancel(context.Background())
	task := &Task{ID: t.next, Label: label, Owner: owner, FG: fg, Started: t.now(), cancel: cancel, ctx: ctx}
	t.tasks[task.ID] = task
	t.order = append(t.order, task.ID)
	return task
}

// Run starts a one-shot task. fn runs off the UI goroutine; its result is
// delivered as TaskMsg{ID, result} followed by TaskEndedMsg.
func (t *Tasks) Run(label, owner string, fg bool, fn func(ctx context.Context) tea.Msg) (int, tea.Cmd) {
	task := t.add(label, owner, fg)
	ctx := task.ctx
	id := task.ID
	return id, func() tea.Msg {
		return taskDoneMsg{ID: id, Msg: fn(ctx)}
	}
}

// Stream starts a streaming task. fn returns a channel the producer closes
// when done; each item is delivered as TaskMsg, then TaskEndedMsg.
func (t *Tasks) Stream(label, owner string, fg bool, fn func(ctx context.Context) (<-chan tea.Msg, error)) (int, tea.Cmd) {
	task := t.add(label, owner, fg)
	ctx := task.ctx
	id := task.ID
	return id, func() tea.Msg {
		ch, err := fn(ctx)
		return streamOpenMsg{ID: id, ch: ch, err: err}
	}
}

func listen(id int, ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return streamEndMsg{ID: id}
		}
		return streamItemMsg{ID: id, Msg: msg, ch: ch}
	}
}

// Relabel renames a running task (shown in the header spinner).
func (t *Tasks) Relabel(id int, label string) {
	if task, ok := t.tasks[id]; ok {
		task.Label = label
	}
}

// Get returns a running task.
func (t *Tasks) Get(id int) *Task { return t.tasks[id] }

// Running reports whether task id is still registered.
func (t *Tasks) Running(id int) bool { _, ok := t.tasks[id]; return ok }

// Cancel cancels a task; it stays registered until its goroutine ends.
func (t *Tasks) Cancel(id int) bool {
	if task, ok := t.tasks[id]; ok {
		task.cancel()
		return true
	}
	return false
}

// CancelOwner cancels every task owned by a screen.
func (t *Tasks) CancelOwner(owner string) {
	for _, task := range t.tasks {
		if task.Owner == owner {
			task.cancel()
		}
	}
}

// CancelAll cancels everything (on quit).
func (t *Tasks) CancelAll() {
	for _, task := range t.tasks {
		task.cancel()
	}
}

// Foreground returns the newest foreground task that isn't already cancelled.
func (t *Tasks) Foreground() *Task {
	for i := len(t.order) - 1; i >= 0; i-- {
		if task, ok := t.tasks[t.order[i]]; ok && task.FG && !task.Canceled() {
			return task
		}
	}
	return nil
}

// ForegroundOf returns the newest live foreground task owned by one of owners.
func (t *Tasks) ForegroundOf(owners ...string) *Task {
	for i := len(t.order) - 1; i >= 0; i-- {
		task, ok := t.tasks[t.order[i]]
		if !ok || !task.FG || task.Canceled() {
			continue
		}
		for _, o := range owners {
			if task.Owner == o {
				return task
			}
		}
	}
	return nil
}

// List returns running tasks oldest first.
func (t *Tasks) List() []*Task {
	var out []*Task
	for _, id := range t.order {
		if task, ok := t.tasks[id]; ok {
			out = append(out, task)
		}
	}
	return out
}

// Len is the number of running tasks.
func (t *Tasks) Len() int { return len(t.tasks) }

func (t *Tasks) finish(id int) (TaskEndedMsg, bool) {
	task, ok := t.tasks[id]
	if !ok {
		return TaskEndedMsg{}, false
	}
	canceled := task.Canceled()
	task.cancel()
	delete(t.tasks, id)
	for i, x := range t.order {
		if x == id {
			t.order = append(t.order[:i], t.order[i+1:]...)
			break
		}
	}
	return TaskEndedMsg{ID: id, Canceled: canceled}, true
}

// Busy reports whether a task with this label is running.
func (t *Tasks) Busy(label string) bool {
	for _, x := range t.tasks {
		if x.Label == label {
			return true
		}
	}
	return false
}
