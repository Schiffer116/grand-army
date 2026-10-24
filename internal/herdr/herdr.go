// Package herdr is the Host adapter for Herdr (ADR 0006): it maps host.Host to
// the Herdr CLI and its JSON. Nothing outside this package knows Herdr's
// commands, wire format or environment variables.
package herdr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"berthier/internal/core"
	"berthier/internal/host"
)

// Herdr runs `herdr`. Session empty means the user's default Herdr session;
// tests point it at an isolated named one so they never touch live workspaces.
type Herdr struct {
	Bin     string
	Session string
}

var _ host.Host = Herdr{}

// FromEnv is the Herdr the BERTHIER_HERDR* variables describe.
func FromEnv(getenv func(string) string) Herdr {
	h := Herdr{Bin: getenv("BERTHIER_HERDR"), Session: getenv("BERTHIER_HERDR_SESSION")}
	if h.Bin == "" {
		h.Bin = "herdr"
	}
	return h
}

// Error is Herdr's own error: its code and message.
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Message }

// Is: a Herdr "..._not_found" code is host.ErrNotFound.
func (e *Error) Is(target error) bool {
	return target == host.ErrNotFound && strings.HasSuffix(e.Code, "_not_found")
}

// command builds a `herdr` command against the session. Inherited pane
// context would otherwise pin us to whichever session spawned this process.
func (h Herdr) command(args ...string) *exec.Cmd {
	if h.Session == "" {
		return exec.Command(h.Bin, args...)
	}
	cmd := exec.Command(h.Bin, append([]string{"--session", h.Session}, args...)...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HERDR_SOCKET_PATH=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	return cmd
}

// run runs a Herdr command and returns its stdout; a failure carries Herdr's
// own error when it gave one.
func (h Herdr) run(args ...string) ([]byte, error) {
	var out, errb bytes.Buffer
	cmd := h.command(args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return out.Bytes(), err
		}
		return out.Bytes(), errorFrom(errb.Bytes(), err)
	}
	return out.Bytes(), nil
}

// errorFrom is Herdr's error from its JSON error output.
func errorFrom(stderr []byte, fallback error) error {
	var e struct {
		Error struct{ Code, Message string }
	}
	if json.Unmarshal(stderr, &e) == nil && e.Error.Message != "" {
		return &Error{e.Error.Code, e.Error.Message}
	}
	if s := strings.TrimSpace(string(stderr)); s != "" {
		return errors.New(s)
	}
	return fallback
}

// json runs a Herdr command and decodes its JSON output into v.
func (h Herdr) json(v any, args ...string) error {
	out, err := h.run(args...)
	if err != nil {
		return err
	}
	return json.Unmarshal(out, v)
}

func (h Herdr) Snapshot() (core.Snapshot, error) {
	var r struct {
		Result struct {
			Snapshot *struct {
				Workspaces []wireWorkspace `json:"workspaces"`
				Agents     []wirePane      `json:"agents"`
				Panes      []wirePane      `json:"panes"`
			}
		}
	}
	if err := h.json(&r, "api", "snapshot"); err != nil || r.Result.Snapshot == nil {
		return core.Snapshot{}, host.ErrUnreachable
	}
	// Herdr's own detection of Agents (status, session) is not used: a pane
	// carries its name, which an Agent's rename may have put on the Agent.
	names := map[string]*string{}
	for _, a := range r.Result.Snapshot.Agents {
		names[a.PaneID] = a.Name
	}
	s := core.Snapshot{Panes: panes(r.Result.Snapshot.Panes, true)}
	for i, p := range s.Panes {
		if n, ok := names[p.PaneID]; ok {
			s.Panes[i].Name = n // an agent forgets its name across a restart: nil, not the pane's label
		}
	}
	for _, w := range r.Result.Snapshot.Workspaces {
		s.Workspaces = append(s.Workspaces, w.core())
	}
	return s, nil
}

func (h Herdr) Workspaces() ([]core.Workspace, error) {
	var r struct {
		Result struct{ Workspaces []wireWorkspace }
	}
	if err := h.json(&r, "workspace", "list"); err != nil {
		return nil, err
	}
	var out []core.Workspace
	for _, w := range r.Result.Workspaces {
		out = append(out, w.core())
	}
	return out, nil
}

func (h Herdr) Workspace(id string) (core.Workspace, error) {
	var r struct {
		Result struct{ Workspace wireWorkspace }
	}
	if err := h.json(&r, "workspace", "get", id); err != nil {
		return core.Workspace{}, err
	}
	return r.Result.Workspace.core(), nil
}

// created is the reply of a command that opens a workspace.
type created struct {
	Result struct {
		RootPane struct {
			PaneID string `json:"pane_id"`
		} `json:"root_pane"`
		Workspace struct {
			ID string `json:"workspace_id"`
		}
	}
}

func (c created) host() (host.Created, error) {
	if c.Result.RootPane.PaneID == "" {
		return host.Created{}, errors.New("herdr opened no pane")
	}
	return host.Created{WorkspaceID: c.Result.Workspace.ID, PaneID: c.Result.RootPane.PaneID}, nil
}

func (h Herdr) CreateWorkspace(cwd, label string) (host.Created, error) {
	var r created
	if err := h.json(&r, "workspace", "create", "--cwd", cwd, "--label", label, "--no-focus"); err != nil {
		return host.Created{}, err
	}
	return r.host()
}

func (h Herdr) CloseWorkspace(id string) error {
	_, err := h.run("workspace", "close", id)
	return err
}

func (h Herdr) RenameWorkspace(id, label string) error {
	_, err := h.run("workspace", "rename", id, label)
	return err
}

func (h Herdr) CreateWorktree(repo, branch, label string) (host.Created, error) {
	var r created
	if err := h.json(&r, "worktree", "create", "--cwd", repo, "--branch", branch, "--label", label, "--no-focus"); err != nil {
		return host.Created{}, err
	}
	return r.host()
}

func (h Herdr) RemoveWorktree(workspace string) error {
	_, err := h.run("worktree", "remove", "--workspace", workspace)
	return err
}

func (h Herdr) Panes(workspace string) ([]core.SnapAgent, error) {
	if workspace != "" {
		if _, err := h.Workspace(workspace); err != nil {
			return nil, err
		}
	}
	snap, err := h.Snapshot()
	if err != nil {
		return nil, err
	}
	var out []core.SnapAgent
	for _, p := range snap.Panes {
		if workspace == "" || p.WorkspaceID == workspace {
			out = append(out, p)
		}
	}
	return out, nil
}

func (h Herdr) Pane(id string) (core.SnapAgent, error) {
	var r struct {
		Result struct{ Pane wirePane }
	}
	if err := h.json(&r, "pane", "get", id); err != nil {
		return core.SnapAgent{}, err
	}
	p := r.Result.Pane.core(true)
	var a struct {
		Result struct{ Agent wirePane }
	}
	if h.json(&a, "agent", "get", id) == nil && a.Result.Agent.Name != nil {
		p.Name = a.Result.Agent.Name
	}
	return p, nil
}

func (h Herdr) Read(pane string, o host.ReadOpts) (string, error) {
	args := []string{"pane", "read", pane, "--source", "visible"}
	if o.Recent > 0 {
		args = []string{"pane", "read", pane, "--source", "recent-unwrapped", "--lines", fmt.Sprint(h.unscrolled(pane, o.Recent))}
	}
	if o.ANSI {
		args = append(args, "--format", "ansi")
	}
	out, err := h.run(args...)
	return string(out), err
}

// unscrolled caps a read of pane at want lines to what its screen and
// scrollback hold: asked for more, Herdr scrolls an idle alt-screen Agent's
// view up and back to collect its history, and the user sees it jump.
func (h Herdr) unscrolled(pane string, want int) int {
	var got struct {
		Result struct {
			Pane struct {
				Scroll struct {
					Rows int `json:"viewport_rows"`
					Back int `json:"max_offset_from_bottom"`
				}
			}
		}
	}
	if h.json(&got, "pane", "get", pane) != nil || got.Result.Pane.Scroll.Rows == 0 {
		return want
	}
	return min(want, got.Result.Pane.Scroll.Rows+got.Result.Pane.Scroll.Back)
}

// Type sends text as a bracketed paste: `pane send-text` alone is raw input,
// which Claude does not treat as pasted (no image attachments).
func (h Herdr) Type(pane, text string) error {
	_, err := h.run("pane", "send-text", pane, "\x1b[200~"+text+"\x1b[201~")
	return err
}

// KeyName is Herdr's name for a physical key.
func KeyName(k host.Key) string {
	switch k {
	case host.KeyEscape:
		return "esc"
	case host.KeyCtrlC:
		return "ctrl+c"
	case host.KeyCtrlU:
		return "ctrl+u"
	}
	return string(k)
}

func (h Herdr) Key(pane string, k host.Key) error {
	_, err := h.run("pane", "send-keys", pane, KeyName(k))
	return err
}

// RenamePane names the pane's Agent when it has one, else the pane itself.
func (h Herdr) RenamePane(pane, name string) error {
	_, err := h.run("agent", "rename", pane, name)
	var e *Error
	if errors.As(err, &e) && e.Code == "agent_not_found" {
		_, err = h.run("pane", "rename", pane, name)
	}
	return err
}

func (h Herdr) Process(pane string) (host.Process, error) {
	var r struct {
		Result struct {
			ProcessInfo struct {
				Foreground []struct{ Name string } `json:"foreground_processes"`
			} `json:"process_info"`
		}
	}
	if err := h.json(&r, "pane", "process-info", "--pane", pane); err != nil {
		return host.Process{}, err
	}
	fg := r.Result.ProcessInfo.Foreground
	if len(fg) == 0 {
		return host.Process{}, nil
	}
	name := strings.TrimPrefix(fg[0].Name, "-")
	return host.Process{Name: name, AtShell: isShell(name)}, nil
}

func isShell(name string) bool {
	switch name {
	case "zsh", "bash", "fish", "sh", "dash", "nu":
		return true
	}
	return false
}

func (h Herdr) Start(pane string, argv []string) error {
	if len(argv) == 0 {
		return errors.New("nothing to start")
	}
	p, err := h.Process(pane)
	if err != nil {
		return err
	}
	if !p.AtShell {
		return fmt.Errorf("pane %s is not at a shell prompt", pane)
	}
	_, err = h.run("pane", "run", pane, shellLine(argv))
	return err
}

// shellLine is argv as one shell command line, each argument one word.
func shellLine(argv []string) string {
	words := make([]string, len(argv))
	for i, a := range argv {
		words[i] = quote(a)
	}
	return strings.Join(words, " ")
}

func quote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789@%+=:,./-_") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Self: Herdr sets the pane and workspace in the environment of every process
// it starts in a pane.
func (Herdr) Self(getenv func(string) string) (pane, workspace string) {
	return getenv("HERDR_PANE_ID"), getenv("HERDR_WORKSPACE_ID")
}

func (h Herdr) Notify(title, body string) error {
	_, err := h.run("notification", "show", title, "--body", body, "--sound", "request")
	return err
}
