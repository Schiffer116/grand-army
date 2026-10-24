// Package host is Berthier's port to the Host, the terminal multiplexer the
// Grand Army lives in (ADR 0006). Herdr is today's adapter. Nothing outside an
// adapter, and the commands of Berthier's own Corps, names the Host product.
//
// The doc comments on Host are the written contract; hosttest.Contract holds
// it as tests, run against the fake (unit tests) and the real adapter (e2e).
//
// The Host knows workspaces, panes and processes. It does not detect what an
// Agent is doing: that comes from the Harness (Claude Code's hooks). Herdr-only
// concepts (tabs, layouts, plugins) stay out of it.
package host

import (
	"errors"

	"berthier/internal/core"
)

var (
	// ErrUnreachable is the Host not answering at all.
	ErrUnreachable = errors.New("host unreachable")
	// ErrNotFound is a workspace or pane that does not exist (any more).
	ErrNotFound = errors.New("not found")
)

// Key is a physical key press, by name. What a key means to the program in
// the pane (interrupt, clear the draft, end the session) is the Harness's
// business, not the Host's; the adapter maps these names to the Host's own.
type Key string

const (
	KeyEscape Key = "escape"
	KeyEnter  Key = "enter"
	KeyCtrlC  Key = "ctrl-c"
	KeyCtrlU  Key = "ctrl-u"
)

// Keys are all the keys, for commands to validate against.
var Keys = []Key{KeyEscape, KeyEnter, KeyCtrlC, KeyCtrlU}

// ReadOpts says what part of a pane's terminal to read.
type ReadOpts struct {
	// Recent is the number of most recent lines, unwrapped, scrollback
	// included. Zero reads the visible screen only (cheap; reading more than
	// the screen holds can make the Host scroll the pane under the user).
	Recent int
	// ANSI keeps the escape sequences (colour, dim), for telling typed text
	// from a suggestion.
	ANSI bool
}

// Process is what runs in a pane's foreground.
type Process struct {
	// Name of the foreground process; empty when the Host reports none.
	Name string
	// AtShell: the foreground process is a bare interactive shell, so the pane
	// is ready for a new Start.
	AtShell bool
}

// Alive: something other than a shell is running in the pane.
func (p Process) Alive() bool { return p.Name != "" && !p.AtShell }

// Created is a new workspace and its first pane.
type Created struct {
	WorkspaceID string
	PaneID      string
}

// Host is the multiplexer. Every method is safe to call from several
// processes at once (the loop, the Console, commands). Methods taking a
// workspace or pane id return an error wrapping ErrNotFound when it does not
// exist. Typed methods only: there is no way to pass raw arguments through.
type Host interface {
	// Snapshot lists every workspace and pane. It is the one call a Main Loop
	// tick makes to observe the Host; ErrUnreachable when the Host is down.
	// (Agent fields of a pane, Status and Session, are transitional: they go
	// when agent state comes from hooks.)
	Snapshot() (core.Snapshot, error)

	// Workspaces lists the workspaces, in the Host's order.
	Workspaces() ([]core.Workspace, error)
	// Workspace is one workspace.
	Workspace(id string) (core.Workspace, error)
	// CreateWorkspace opens a workspace labelled label with one pane at cwd,
	// without taking the user's focus.
	CreateWorkspace(cwd, label string) (Created, error)
	// CloseWorkspace closes a workspace and its panes, killing what runs in them.
	CloseWorkspace(id string) error
	// RenameWorkspace relabels a workspace.
	RenameWorkspace(id, label string) error
	// CreateWorktree creates a git worktree of repo on a new branch and opens
	// a workspace labelled label in it, without taking focus. The workspace's
	// Worktree is set.
	CreateWorktree(repo, branch, label string) (Created, error)
	// RemoveWorktree closes a worktree workspace and removes its checkout. It
	// fails, leaving everything in place, when the checkout has uncommitted
	// changes.
	RemoveWorktree(workspace string) error

	// Panes lists the panes of a workspace, or of every workspace when it is "".
	Panes(workspace string) ([]core.SnapAgent, error)
	// Pane is one pane.
	Pane(id string) (core.SnapAgent, error)
	// Read returns the pane's terminal text, as ReadOpts says.
	Read(pane string, o ReadOpts) (string, error)
	// Type puts text into the pane's input as typed, without pressing Enter.
	// Text may hold newlines.
	Type(pane, text string) error
	// Key presses a physical key in the pane.
	Key(pane string, k Key) error
	// RenamePane gives the pane (and so its Agent) a name.
	RenamePane(pane, name string) error
	// Process is what the pane's foreground runs. A pane the Host reports no
	// process for is the zero Process.
	Process(pane string) (Process, error)
	// Start runs argv in a pane that is at a shell prompt, as the shell would
	// (no shell syntax: each argument is one word). It returns once the
	// process was launched, not once it finished. The process and its
	// children learn their pane with Self.
	Start(pane string, argv []string) error
	// Self says which pane and workspace a process runs in, from its
	// environment, as the Host set it up for processes it started. Both are
	// "" outside the Host.
	Self(getenv func(string) string) (pane, workspace string)

	// Notify shows the user a notification in the Host's own UI, with a sound.
	Notify(title, body string) error
}
