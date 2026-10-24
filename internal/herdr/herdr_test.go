package herdr

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"berthier/internal/host"
	"berthier/internal/host/hosttest"
)

// The Host contract against a real Herdr, in the named test session
// BERTHIER_CONTRACT_HERDR_SESSION (tests/e2e.sh sets it); never the user's own.
func TestHostContract(t *testing.T) {
	session := os.Getenv("BERTHIER_CONTRACT_HERDR_SESSION")
	if session == "" {
		t.Skip("BERTHIER_CONTRACT_HERDR_SESSION is not set (tests/e2e.sh runs this)")
	}
	hosttest.Contract(t, func(t *testing.T) host.Host { return Herdr{Bin: "herdr", Session: session} })
}

func TestShellLine(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{"claude", "--resume", "0b6f-2c1e", "--model", "opus"}, "claude --resume 0b6f-2c1e --model opus"},
		{[]string{"sh", "-c", `env > "$0.tmp"; sleep 60`, "/tmp/x y"}, `sh -c 'env > "$0.tmp"; sleep 60' '/tmp/x y'`},
		{[]string{"echo", "it's", ""}, `echo 'it'\''s' ''`},
	} {
		if got := shellLine(c.argv); got != c.want {
			t.Errorf("shellLine(%q) = %s, want %s", c.argv, got, c.want)
		}
	}
}

func TestKeyNames(t *testing.T) {
	for k, want := range map[host.Key]string{host.KeyEscape: "esc", host.KeyEnter: "enter", host.KeyCtrlC: "ctrl+c", host.KeyCtrlU: "ctrl+u"} {
		if got := KeyName(k); got != want {
			t.Errorf("KeyName(%s) = %s, want %s", k, got, want)
		}
	}
	for _, k := range host.Keys {
		if KeyName(k) == "" {
			t.Errorf("no Herdr name for %s", k)
		}
	}
}

// Herdr's "..._not_found" errors are host.ErrNotFound, with Herdr's own message.
func TestErrors(t *testing.T) {
	err := errorFrom([]byte(`{"error":{"code":"pane_not_found","message":"pane x:p1 not found"},"id":"cli:pane:get"}`), errors.New("exit 1"))
	if !errors.Is(err, host.ErrNotFound) || err.Error() != "pane x:p1 not found" {
		t.Errorf("not found: %v", err)
	}
	err = errorFrom([]byte(`{"error":{"code":"dirty_worktree_requires_force","message":"modified files"}}`), errors.New("exit 1"))
	if errors.Is(err, host.ErrNotFound) || err.Error() != "modified files" {
		t.Errorf("other error: %v", err)
	}
	if err := errorFrom([]byte("boom\n"), errors.New("exit 1")); err.Error() != "boom" {
		t.Errorf("plain stderr: %v", err)
	}
	if err := errorFrom(nil, errors.New("exit 1")); err.Error() != "exit 1" {
		t.Errorf("no stderr: %v", err)
	}
}

// What Herdr says of a pane maps to core's types: no JSON tags there, so this
// is the one place the wire shape is read. What Herdr detects of an Agent
// (status, session) is not read.
func TestWireMapping(t *testing.T) {
	var agent wirePane
	json.Unmarshal([]byte(`{"pane_id":"w3:p1","workspace_id":"w3","agent":"claude","agent_status":"idle","state_change_seq":4,
		"agent_session":{"value":"s1"},"name":"demo","cwd":"/r","terminal_title_stripped":"Claude Code","tab_id":"w3:t1"}`), &agent)
	a := agent.core(true)
	if a.PaneID != "w3:p1" || a.Agent != nil || a.Status != "" || a.StateSeq != nil || a.Session != nil || *a.Name != "demo" || *a.Cwd != "/r" || *a.Title != "Claude Code" {
		t.Errorf("pane: %+v", a)
	}
	var pane wirePane
	json.Unmarshal([]byte(`{"pane_id":"w2:p1","workspace_id":"w2","label":"foo"}`), &pane)
	if p := pane.core(true); p.Name == nil || *p.Name != "foo" || p.Agent != nil || p.Session != nil {
		t.Errorf("pane named by its label: %+v", p)
	}
	if p := pane.core(false); p.Name != nil {
		t.Errorf("a snapshot agent is named by its name only: %+v", p)
	}
	var ws wireWorkspace
	json.Unmarshal([]byte(`{"workspace_id":"w5","label":"wtb","number":5,"worktree":{"repo_root":"/r","checkout_path":"/w/wtb","is_linked_worktree":true}}`), &ws)
	if w := ws.core(); w.ID != "w5" || w.Number != 5 || *w.Worktree.RepoRoot != "/r" || *w.Worktree.CheckoutPath != "/w/wtb" {
		t.Errorf("workspace: %+v", w)
	}
}

func TestSelf(t *testing.T) {
	env := map[string]string{"HERDR_PANE_ID": "w1:p1", "HERDR_WORKSPACE_ID": "w1"}
	if p, w := (Herdr{}).Self(func(k string) string { return env[k] }); p != "w1:p1" || w != "w1" {
		t.Errorf("Self = %s, %s", p, w)
	}
}
