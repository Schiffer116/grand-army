package hosttest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"berthier/internal/host"
)

// Contract is the behaviour every Host adapter and the Fake share. newHost
// returns a Host with nothing of ours in it; the suite creates its own
// workspaces and closes them. Terminal effects are awaited, since a real
// Host shows them a moment later.
func Contract(t *testing.T, newHost func(t *testing.T) host.Host) {
	t.Run("workspaces", func(t *testing.T) { workspaces(t, newHost(t)) })
	t.Run("worktrees", func(t *testing.T) { worktrees(t, newHost(t)) })
	t.Run("panes", func(t *testing.T) { panes(t, newHost(t)) })
	t.Run("processes", func(t *testing.T) { processes(t, newHost(t)) })
	t.Run("notify", func(t *testing.T) {
		if err := newHost(t).Notify("berthier contract", "ignore me"); err != nil {
			t.Fatal(err)
		}
	})
}

// eventually polls cond for up to 15s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// workspace creates a workspace in a fresh directory, closed at cleanup.
func workspace(t *testing.T, h host.Host, label string) (host.Created, string) {
	t.Helper()
	dir := t.TempDir()
	c, err := h.CreateWorkspace(dir, label)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.CloseWorkspace(c.WorkspaceID) })
	if c.WorkspaceID == "" || c.PaneID == "" {
		t.Fatalf("CreateWorkspace returned %+v", c)
	}
	return c, dir
}

func workspaces(t *testing.T, h host.Host) {
	c, dir := workspace(t, h, "contract-a")
	ws, err := h.Workspace(c.WorkspaceID)
	if err != nil || ws.ID != c.WorkspaceID || ws.Label != "contract-a" {
		t.Fatalf("Workspace = %+v, %v", ws, err)
	}
	if ws.Worktree != nil {
		t.Errorf("a plain workspace has a Worktree: %+v", ws.Worktree)
	}
	found := false
	all, err := h.Workspaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range all {
		found = found || w.ID == c.WorkspaceID
	}
	if !found {
		t.Errorf("Workspaces does not list %s", c.WorkspaceID)
	}
	snap, err := h.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	inSnap := false
	for _, p := range snap.Panes {
		if p.PaneID == c.PaneID {
			inSnap = p.WorkspaceID == c.WorkspaceID && p.Cwd != nil && *p.Cwd == dir
		}
	}
	if !inSnap {
		t.Errorf("Snapshot lacks pane %s of %s at %s", c.PaneID, c.WorkspaceID, dir)
	}

	if err := h.RenameWorkspace(c.WorkspaceID, "contract-b"); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.Workspace(c.WorkspaceID); got.Label != "contract-b" {
		t.Errorf("label after rename = %q", got.Label)
	}

	if err := h.CloseWorkspace(c.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Workspace(c.WorkspaceID); !errors.Is(err, host.ErrNotFound) {
		t.Errorf("Workspace after close: %v, want ErrNotFound", err)
	}
	if _, err := h.Pane(c.PaneID); !errors.Is(err, host.ErrNotFound) {
		t.Errorf("Pane after close: %v, want ErrNotFound", err)
	}
	if err := h.CloseWorkspace(c.WorkspaceID); !errors.Is(err, host.ErrNotFound) {
		t.Errorf("second CloseWorkspace: %v, want ErrNotFound", err)
	}
	if err := h.RenameWorkspace(c.WorkspaceID, "x"); !errors.Is(err, host.ErrNotFound) {
		t.Errorf("RenameWorkspace of a closed workspace: %v, want ErrNotFound", err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Env = filterGit(os.Environ())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// filterGit drops the variables a git hook exports, which would point the
// test's git at the real repo.
func filterGit(env []string) []string {
	var out []string
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k == "GIT_DIR" || k == "GIT_INDEX_FILE" || k == "GIT_WORK_TREE" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func worktrees(t *testing.T, h host.Host) {
	// A Host may put checkouts under a directory named after the repo, so the
	// name is unique to this run.
	repo, err := os.MkdirTemp("", "hosttest-repo-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(repo) })
	if repo, err = filepath.EvalSymlinks(repo); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q")
	os.WriteFile(filepath.Join(repo, "f.txt"), []byte("one\n"), 0o644)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "first")

	c, err := h.CreateWorktree(repo, "contract-wt", "contract-wt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.CloseWorkspace(c.WorkspaceID) })
	got, err := h.Workspace(c.WorkspaceID)
	if err != nil || got.Label != "contract-wt" || got.Worktree == nil || got.Worktree.CheckoutPath == nil {
		t.Fatalf("worktree workspace = %+v, %v", got, err)
	}
	if got.Worktree.RepoRoot == nil || *got.Worktree.RepoRoot != repo {
		t.Errorf("RepoRoot = %v, want %s", got.Worktree.RepoRoot, repo)
	}
	path := *got.Worktree.CheckoutPath
	if _, err := os.Stat(filepath.Join(path, "f.txt")); err != nil {
		t.Errorf("the checkout lacks the repo's files: %v", err)
	}

	// Uncommitted changes keep the checkout and the workspace.
	os.WriteFile(filepath.Join(path, "f.txt"), []byte("two\n"), 0o644)
	if err := h.RemoveWorktree(c.WorkspaceID); err == nil {
		t.Fatal("RemoveWorktree succeeded with uncommitted changes")
	}
	if _, err := h.Workspace(c.WorkspaceID); err != nil {
		t.Errorf("a refused RemoveWorktree closed the workspace: %v", err)
	}
	git(t, path, "checkout", "-q", "--", "f.txt")
	if err := h.RemoveWorktree(c.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Workspace(c.WorkspaceID); !errors.Is(err, host.ErrNotFound) {
		t.Errorf("Workspace after RemoveWorktree: %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the checkout is still there: %v", err)
	}
}

func screen(h host.Host, pane string, o host.ReadOpts) string {
	s, _ := h.Read(pane, o)
	return s
}

func panes(t *testing.T, h host.Host) {
	c, _ := workspace(t, h, "contract-panes")
	pane := c.PaneID
	eventually(t, "the shell", func() bool { p, _ := h.Process(pane); return p.AtShell })

	list, err := h.Panes(c.WorkspaceID)
	if err != nil || len(list) != 1 || list[0].PaneID != pane {
		t.Fatalf("Panes(workspace) = %+v, %v", list, err)
	}
	if p, err := h.Pane(pane); err != nil || p.PaneID != pane || p.WorkspaceID != c.WorkspaceID {
		t.Fatalf("Pane = %+v, %v", p, err)
	}
	if _, err := h.Panes("nope"); !errors.Is(err, host.ErrNotFound) {
		t.Errorf("Panes of no workspace: %v, want ErrNotFound", err)
	}
	if _, err := h.Pane("nope:p1"); !errors.Is(err, host.ErrNotFound) {
		t.Errorf("Pane of no pane: %v, want ErrNotFound", err)
	}
	if _, err := h.Read("nope:p1", host.ReadOpts{}); !errors.Is(err, host.ErrNotFound) {
		t.Errorf("Read of no pane: %v, want ErrNotFound", err)
	}

	if err := h.RenamePane(pane, "contract-name"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the pane's name", func() bool {
		p, _ := h.Pane(pane)
		return p.Name != nil && *p.Name == "contract-name"
	})

	// Type puts text in the input without submitting it.
	if err := h.Type(pane, "echo draft-one"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "typed text on screen", func() bool { return strings.Contains(screen(h, pane, host.ReadOpts{}), "draft-one") })
	if err := h.Key(pane, host.KeyCtrlU); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the draft gone", func() bool { return !strings.Contains(screen(h, pane, host.ReadOpts{}), "draft-one") })

	// Enter submits what was typed; Recent reads past the screen, ANSI is accepted.
	if err := h.Type(pane, "echo marker-two"); err != nil {
		t.Fatal(err)
	}
	if err := h.Key(pane, host.KeyEnter); err != nil {
		t.Fatal(err)
	}
	// The command line and its output: twice.
	eventually(t, "the command's output", func() bool {
		return strings.Count(screen(h, pane, host.ReadOpts{Recent: 50}), "marker-two") >= 2
	})
	if _, err := h.Read(pane, host.ReadOpts{ANSI: true}); err != nil {
		t.Errorf("Read with ANSI: %v", err)
	}
	if n := len(strings.Split(screen(h, pane, host.ReadOpts{Recent: 3}), "\n")); n > 3 {
		t.Errorf("Read(Recent: 3) returned %d lines", n)
	}
}

func processes(t *testing.T, h host.Host) {
	c, _ := workspace(t, h, "contract-proc")
	pane := c.PaneID
	eventually(t, "the shell", func() bool { p, _ := h.Process(pane); return p.AtShell })
	if p, _ := h.Process(pane); p.Alive() {
		t.Errorf("a shell is not Alive: %+v", p)
	}

	// Start runs a process that learns its pane (Self) from its environment.
	envFile := filepath.Join(t.TempDir(), "env")
	if err := h.Start(pane, []string{"sh", "-c", `env > "$0.tmp" && mv "$0.tmp" "$0"; sleep 60`, envFile}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the process alive", func() bool { p, _ := h.Process(pane); return p.Alive() && !p.AtShell })
	var env []byte
	eventually(t, "the process's environment", func() bool { var err error; env, err = os.ReadFile(envFile); return err == nil })
	vars := map[string]string{}
	for _, kv := range strings.Split(string(env), "\n") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			vars[k] = v
		}
	}
	gotPane, gotWS := h.Self(func(k string) string { return vars[k] })
	if gotPane != pane || gotWS != c.WorkspaceID {
		t.Errorf("Self = %q, %q; want %q, %q", gotPane, gotWS, pane, c.WorkspaceID)
	}
	if p, w := h.Self(func(string) string { return "" }); p != "" || w != "" {
		t.Errorf("Self outside the Host = %q, %q", p, w)
	}

	// Start needs a shell prompt.
	if err := h.Start(pane, []string{"true"}); err == nil {
		t.Error("Start in a pane that is not at a shell succeeded")
	}
	// Escape is harmless to a shell command; Ctrl-C ends it: back at the shell.
	if err := h.Key(pane, host.KeyEscape); err != nil {
		t.Fatal(err)
	}
	if p, _ := h.Process(pane); !p.Alive() {
		t.Errorf("Escape ended the process: %+v", p)
	}
	if err := h.Key(pane, host.KeyCtrlC); err != nil {
		t.Fatal(err)
	}
	eventually(t, "back at the shell", func() bool { p, _ := h.Process(pane); return p.AtShell })
	if err := h.Start(pane, []string{"true"}); err != nil {
		t.Errorf("Start after the shell is back: %v", err)
	}
}
