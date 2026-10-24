package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"berthier/internal/core"
	"berthier/internal/host"
	"berthier/internal/host/hosttest"
)

func TestFreshStart(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "takes")
	e := s.load()
	s.write("reports/old.json", `{"line":"halfway through the parser","pane":"`+pane+`","at":1}`)
	if err := e.saveBrief("dark-mode", "Build dark mode."); err != nil {
		t.Fatal(err)
	}
	f := core.FreshStart{PaneID: pane, Name: "skipli-dark-mode", Session: "old", Model: "claude-sonnet-5-5", Division: "dark-mode", Live: true, Ev: core.Fields{"pane": pane}}

	e.freshStart(f)
	keys := 0
	for _, c := range s.host.CallsTo("Key") {
		if c.Args[1] == "ctrl-c" {
			keys++
		}
	}
	eq(t, "a live Claude is ended (two Ctrl-C)", keys, 2)
	eq(t, "then a new session on the same model", startArgs(s), "fake-agent --model claude-sonnet-5-5")
	eq(t, "named", s.host.CallsTo("RenamePane")[0].String(), "RenamePane "+pane+" skipli-dark-mode")
	eq(t, "prompt", s.sim.Took(pane), core.FreshPrompt("halfway through the parser", "Build dark mode."))
	eq(t, "logged", strings.Contains(s.read("loop.log"), `"kind":"fresh_started"`), true)

	// Not at a shell: failed, the user told.
	s = newSandbox(t)
	pane = fakeHost(t, s, "takes")
	s.host.Update(pane, func(p *hosttest.Pane) { p.Proc = host.Process{Name: "claude"} })
	e = s.load()
	f.PaneID, f.Live = pane, false
	e.freshStart(f)
	eq(t, "no shell: failed, the user told", []any{len(s.host.CallsTo("Start")), len(s.host.Notifications()), strings.Contains(s.read("loop.log"), `"kind":"fresh_failed"`)}, "[0 1 true]")
}

func TestThawRequests(t *testing.T) {
	s := newSandbox(t)
	e := s.load()
	s.write("state.json", `{"agents":{"w1:p1":{"status":"gone","workspace_id":"w1","frozen":{"since":1,"why":"crash","last_at":0,"context":90000}},"w1:p2":{"status":"idle","workspace_id":"w1"}}}`)
	if err := e.Thaw("w1:p2", "revive"); err == nil || !strings.Contains(err.Error(), "not a Frozen Agent") {
		t.Errorf("thawing an Agent not Frozen: %v", err)
	}
	if err := e.Thaw("w1:p1", "melt"); err == nil {
		t.Error("an unknown choice was taken")
	}
	e.Thaw("w1:p1", "revive")
	e.Thaw("w1:p1", "fresh")
	got := e.ReadThaws()
	eq(t, "the latest choice per pane", []any{len(got), got[0].Pane, got[0].How, got[0].ID}, "[1 w1:p1 fresh w1_p1]")
	_, errs, code := s.sh("", "fresh", "w1:p2")
	eq(t, "command refuses", []any{strings.TrimSpace(errs), code}, "[berthier: w1:p2 is not a Frozen Agent 1]")
}

func TestWake(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "takes")
	s.sim.Attach(pane)
	e := s.load()
	s.write("state.json", `{"agents":{"`+pane+`":{"status":"idle","name":"skipli-dark-mode","session_id":"old","workspace_label":"skipli-dark-mode"}}}`)
	s.write("reports/old.json", `{"line":"halfway through the parser","pane":"`+pane+`","at":1}`)
	if err := e.saveBrief("skipli-dark-mode", "Build dark mode."); err != nil {
		t.Fatal(err)
	}
	s.write("inbox/w1.json", `{}`)
	d := core.Delivery{ID: "w1", PaneID: pane, From: "service", To: "skipli-dark-mode", Text: "[berthier wake] up", Wake: true, Ev: core.Fields{"pane": pane}}

	e.wake(d)
	eq(t, "clear, then rename, then prompt", hostCalls(s), "Read,Type,Key,RenamePane,Read,Type,Key")
	eq(t, "self-sufficient prompt", s.sim.Took(pane), "[berthier wake] up"+core.Rebuild("halfway through the parser", "Build dark mode."))
	_, err := os.Stat(filepath.Join(e.Inbox, "w1.json"))
	eq(t, "delivered and dequeued", []any{os.IsNotExist(err), strings.Contains(s.read("loop.log"), `"kind":"delivered"`)}, "[true true]")

	s = newSandbox(t)
	pane = fakeHost(t, s, "noclear")
	s.sim.Attach(pane)
	e = s.load()
	s.write("state.json", `{"agents":{"`+pane+`":{"status":"idle","name":"skipli-dark-mode","session_id":"old","workspace_label":"skipli-dark-mode"}}}`)
	s.write("inbox/w1.json", `{}`)
	d.PaneID = pane
	e.wake(d)
	_, err = os.Stat(filepath.Join(e.Inbox, "w1.json"))
	eq(t, "unconfirmed clear: nothing more sent, retried", []any{len(s.host.CallsTo("RenamePane")), s.sim.Took(pane), err == nil, strings.Contains(s.read("loop.log"), `"kind":"wake_clear_failed"`)}, "[0 /clear true true]")

	s = newSandbox(t)
	pane = fakeHost(t, s, "typing")
	s.sim.Attach(pane)
	e = s.load()
	d.PaneID = pane
	e.wake(d)
	eq(t, "a draft blocks the clear", len(s.host.CallsTo("Type")), 0)
}
