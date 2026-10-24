package app

import (
	"strings"
	"testing"

	"berthier/internal/host"
	"berthier/internal/host/hosttest"
)

func TestCheckInCommand(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "typing")
	s.sim.Attach(pane)
	out, errs, code := s.sh("", "check-in", pane)
	if code != 0 || !strings.Contains(out, "half a thought") {
		t.Errorf("check-in: %d %q %q", code, out, errs)
	}
	out, _, code = s.sh("", "check-in", pane, "--recent", "5")
	if code != 0 || !strings.Contains(out, "history") {
		t.Errorf("check-in --recent: %d %q", code, out)
	}
	if c := s.host.CallsTo("Read"); c[len(c)-1].Args[0] != pane {
		t.Errorf("read %v", c)
	}
	for _, args := range [][]string{{"check-in"}, {"check-in", pane, "--recent", "x"}, {"check-in", "nope:p9"}} {
		if _, _, code := s.sh("", args...); code != 1 {
			t.Errorf("%v: code %d", args, code)
		}
	}
}

func TestArmyPaneCommand(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "takes")
	s.sim.Attach(pane)
	e := s.load()
	if err := e.Tick(); err != nil {
		t.Fatal(err)
	}
	out, errs, code := s.sh("", "army", pane)
	if code != 0 {
		t.Fatalf("army: %d %q", code, errs)
	}
	eq(t, "state", jq(t, out, "state"), `"idle"`)
	eq(t, "corps", jq(t, out, "corps"), `"demo"`)
	eq(t, "process", jq(t, out, "process"), `"alive"`)
	eq(t, "session", jq(t, out, "session"), `"s1"`)
	// A shell is not an Agent: unknown, at shell.
	s.host.Update(pane, func(p *hosttest.Pane) { p.Proc = host.Process{Name: "sh", AtShell: true} })
	other, _ := s.host.CreateWorkspace(t.TempDir(), "side")
	out, _, _ = s.sh("", "army", other.PaneID)
	eq(t, "not an Agent", jq(t, out, "state")+jq(t, out, "process")+jq(t, out, "corps"), `"unknown""at shell""side"`)
	if _, _, code := s.sh("", "army", "nope:p9"); code != 1 {
		t.Errorf("army of no pane: %d", code)
	}
	if out, _, _ := s.sh("", "army"); !strings.Contains(out, `"corps"`) {
		t.Errorf("army: %q", out)
	}
}

func TestKeysAndRenameCommands(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "typing")
	s.sim.Attach(pane)
	keys := func() []string {
		var out []string
		for _, c := range s.host.CallsTo("Key") {
			out = append(out, c.Args[1])
		}
		return out
	}
	s.sh("", "keys", pane, "interrupt")
	s.sh("", "keys", pane, "enter")
	eq(t, "named keys", keys(), "[escape enter]")
	if _, _, code := s.sh("", "keys", pane, "clear-draft"); code != 0 {
		t.Errorf("clear-draft: %d", code)
	}
	if got, _ := s.host.Read(pane, host.ReadOpts{ANSI: true}); strings.Contains(got, "half a thought") {
		t.Errorf("the draft is still there: %q", got)
	}
	for _, args := range [][]string{{"keys", pane}, {"keys", pane, "ctrl-c"}, {"keys", "nope:p9", "enter"}} {
		if _, _, code := s.sh("", args...); code != 1 {
			t.Errorf("%v: code %d", args, code)
		}
	}
	if _, _, code := s.sh("", "rename", pane, "scout"); code != 0 {
		t.Errorf("rename: %d", code)
	}
	eq(t, "renamed", s.host.CallsTo("RenamePane")[0].String(), "RenamePane "+pane+" scout")
	if _, _, code := s.sh("", "rename", pane); code != 1 {
		t.Errorf("rename without a name: %d", code)
	}
}
