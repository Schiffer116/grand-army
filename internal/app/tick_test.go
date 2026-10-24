package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"berthier/internal/core"
	"berthier/internal/host"
	"berthier/internal/host/hosttest"
)

// events are the loop log's JSON lines of one kind.
func events(s *sandbox, kind string) []string {
	var out []string
	for _, l := range strings.Split(s.read("loop.log"), "\n") {
		if jq0(l, "kind") == kind {
			out = append(out, l)
		}
	}
	return out
}

// A whole Env.Tick on the fakes: an Agent runs, then its process dies and the
// pane is back at a shell; the next tick Revives it, resuming its session in
// the same pane under its old name.
func TestTickRevivesADeadAgent(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "takes")
	s.sim.Attach(pane)
	s.host.Update(pane, func(p *hosttest.Pane) { name := "demo"; p.Name = &name })
	e := s.load()
	// Claude can only resume a session it saved: a transcript with a recent call.
	tr := filepath.Join(s.home, "claude", "projects", "p")
	os.MkdirAll(tr, 0o755)
	os.WriteFile(filepath.Join(tr, "s1.jsonl"), []byte(fmt.Sprintf("user hi\ncall %d fake-1 1000\n", e.now())), 0o644)

	if err := e.Tick(); err != nil {
		t.Fatal(err)
	}
	eq(t, "the Agent is in the state", jq(t, s.read("state.json"), "agents."+pane+".status"), `"idle"`)
	eq(t, "nothing to Revive", len(s.host.CallsTo("Start")), 0)

	// Its process dies: no Agent listed, the pane back at its shell.
	s.host.Update(pane, func(p *hosttest.Pane) {
		p.Proc = host.Process{Name: "sh", AtShell: true}
		p.Lines = []string{"zsh: killed     claude"}
	})
	if err := e.Tick(); err != nil {
		t.Fatal(err)
	}
	eq(t, "resumed in its pane", startArgs(s), "fake-agent --resume s1 --model fake-1")
	if got := s.host.CallsTo("Start")[0].Args[0]; got != pane {
		t.Errorf("started in %s, want %s", got, pane)
	}
	rev := events(s, "revived")
	if len(rev) != 1 || jq(t, rev[0], "pane") != `"`+pane+`"` || jq(t, rev[0], "session") != `"s1"` {
		t.Fatalf("revived events: %v\nlog:\n%s", rev, s.read("loop.log"))
	}
	eq(t, "named again", s.host.CallsTo("RenamePane")[0].String(), "RenamePane "+pane+" demo")
	eq(t, "not given up on", len(events(s, "revive_failed")), 0)
}

// A deliberate exit is never Revived.
func TestTickDoesNotReviveADeliberateExit(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "takes")
	s.sim.Attach(pane)
	e := s.load()
	tr := filepath.Join(s.home, "claude", "projects", "p")
	os.MkdirAll(tr, 0o755)
	os.WriteFile(filepath.Join(tr, "s1.jsonl"), []byte(fmt.Sprintf("call %d fake-1 1000\n", e.now())), 0o644)
	if err := e.Tick(); err != nil {
		t.Fatal(err)
	}
	s.host.Update(pane, func(p *hosttest.Pane) {
		p.Proc = host.Process{Name: "sh", AtShell: true}
		p.Lines = []string{"bye"}
	})
	s.recordExit(pane, "s1", "exited", "SessionEnd", "exit")
	if err := e.Tick(); err != nil {
		t.Fatal(err)
	}
	eq(t, "no start", len(s.host.CallsTo("Start")), 0)
	eq(t, "forgotten", len(events(s, "forgot")), 1)
}

// A Host that is down is a failed tick, not a crash: the loop logs the outage.
func TestTickHostDown(t *testing.T) {
	s := newSandbox(t)
	fakeHost(t, s, "takes")
	s.host.Down = true
	e := s.load()
	if err := e.Tick(); err != ErrHostUnreachable {
		t.Errorf("Tick = %v, want ErrHostUnreachable", err)
	}
}

// A restarted Agent runs on the model its transcript last called; the Division's
// recorded model is the fallback and follows the transcript.
func TestStickyModel(t *testing.T) {
	s := newSandbox(t)
	fakeHost(t, s, "takes")
	e := s.load()
	e.SetDivision("feat", &core.Division{Corps: "demo", Model: "haiku"})
	tr := filepath.Join(s.home, "claude", "projects", "p")
	os.MkdirAll(tr, 0o755)
	eq(t, "no transcript: recorded", e.stickyModel("feat", "s9"), "haiku")
	os.WriteFile(filepath.Join(tr, "s9.jsonl"), []byte(fmt.Sprintf("user hi\ncall %d fake-1 1000\n", e.now())), 0o644)
	eq(t, "transcript wins", e.stickyModel("feat", "s9"), "fake-1")
	eq(t, "record follows", e.ModelOf("feat"), "fake-1")
}

// Herdr forgets the name it was given across a restart: the next tick gives it back.
func TestTickRestoresAForgottenName(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "takes")
	s.sim.Attach(pane)
	s.host.Update(pane, func(p *hosttest.Pane) { name := "demo"; p.Name = &name })
	e := s.load()
	if err := e.Tick(); err != nil {
		t.Fatal(err)
	}
	s.host.Update(pane, func(p *hosttest.Pane) { p.Name = nil })
	if err := e.Tick(); err != nil {
		t.Fatal(err)
	}
	eq(t, "named again", len(events(s, "renamed")), 1)
}

// Cutover: Claudes started before the hooks existed have no record. The new
// loop's first tick keeps them as the last tick saw them, instead of finding
// them vanished and Reviving; a hook then takes over.
func TestTickCutoverKeepsUnrecordedAgents(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "takes")
	s.sim.Attach(pane)
	s.host.Update(pane, func(p *hosttest.Pane) { name := "demo"; p.Name = &name })
	e := s.load()
	tr := filepath.Join(s.home, "claude", "projects", "p")
	os.MkdirAll(tr, 0o755)
	os.WriteFile(filepath.Join(tr, "s1.jsonl"), []byte(fmt.Sprintf("user hi\ncall %d fake-1 1000\n", e.now())), 0o644)
	// What the old build's last tick wrote: the Agent, idle, from Herdr's status.
	if err := e.Tick(); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(e.Agents) // the hooks never spoke for it
	os.MkdirAll(e.Agents, 0o755)
	for i := 0; i < 3; i++ {
		if err := e.Tick(); err != nil {
			t.Fatal(err)
		}
	}
	eq(t, "still its Agent, as it was", jq(t, s.read("state.json"), "agents."+pane+".status"), `"idle"`)
	eq(t, "no Revive, not even a skipped one", len(events(s, "revive_skipped"))+len(events(s, "revived"))+len(s.host.CallsTo("Start")), 0)
	// A hook takes over.
	s.record(pane, "s1", "working", 1)
	e.Tick()
	eq(t, "hook state wins", jq(t, s.read("state.json"), "agents."+pane+".status"), `"working"`)
	// A pane that was never an Agent stays none.
	other, _ := s.host.CreateWorkspace(t.TempDir(), "other")
	s.host.Update(other.PaneID, func(p *hosttest.Pane) { p.Proc = host.Process{Name: "claude"} })
	e.Tick()
	eq(t, "unknown claude pane is none", jq(t, s.read("state.json"), "agents."+other.PaneID), "null")
}
