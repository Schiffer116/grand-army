package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"berthier/internal/host"
	"berthier/internal/host/hosttest"
)

// usageCommands are the commands the usage text lists.
func usageCommands() []string {
	var l []string
	for _, line := range strings.Split(usageText, "\n")[2:] {
		if line != "" && line[0] != ' ' {
			l = append(l, strings.Fields(line)[0])
		}
	}
	return l
}

func registered(cmd string) bool {
	_, ok := commands[cmd]
	return ok || cmd == "sample" // sample is dispatched before the map: it must stay cheap
}

func TestUsageCommandsAreRegistered(t *testing.T) {
	list := usageCommands()
	if len(list) < 15 {
		t.Fatalf("parsed only %v from the usage text", list)
	}
	for _, c := range list {
		if !registered(c) {
			t.Errorf("usage lists %q but no command is registered", c)
		}
	}
	// And the other way: nothing registered is missing from the usage text.
	for c := range commands {
		if c != "brief-after-dialog" && !slices.Contains(list, c) {
			t.Errorf("command %q is not in the usage text", c)
		}
	}
}

func TestPromptCommand(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "takes")
	s.sim.Attach(pane)
	_, errs, code := s.sh("", "prompt", pane, "go on")
	if code != 0 || errs != "" || s.sim.Took(pane) != "go on" {
		t.Errorf("code %d %q prompted %q", code, errs, s.sim.Took(pane))
	}
	s = newSandbox(t)
	pane = fakeHost(t, s, "error")
	s.sim.Attach(pane)
	_, errs, code = s.sh("", "prompt", pane, "hi")
	if code != 1 || !strings.Contains(errs, "agent target x not found") {
		t.Errorf("error case: %d %q", code, errs)
	}
	// Never into the user's draft: refused, nothing typed.
	s = newSandbox(t)
	pane = fakeHost(t, s, "typing")
	s.sim.Attach(pane)
	_, errs, code = s.sh("", "prompt", pane, "hi")
	if code != 1 || !strings.Contains(errs, "input box holds the user's draft") || len(s.host.CallsTo("Type")) != 0 {
		t.Errorf("draft case: %d %q calls %q", code, errs, hostCalls(s))
	}
	_, errs, code = s.sh("", "prompt", pane)
	if code != 1 || !strings.Contains(errs, "usage: berthier prompt") {
		t.Errorf("usage case: %d %q", code, errs)
	}
	s = newSandbox(t)
	pane = fakeHost(t, s, "takes")
	s.sim.Attach(pane)
	_, errs, code = s.sh("line one\nline \"two\"\n", "prompt", pane, "-")
	if code != 0 || errs != "" || s.sim.Took(pane) != "line one\nline \"two\"" {
		t.Errorf("stdin: %d %q prompted %q", code, errs, s.sim.Took(pane))
	}
	_, errs, code = s.sh("\n", "prompt", pane, "-")
	if code != 1 || !strings.Contains(errs, "usage: berthier prompt") {
		t.Errorf("empty stdin: %d %q", code, errs)
	}
}

func TestLastCommand(t *testing.T) {
	s := newSandbox(t)
	s.host = hosttest.New()
	c, _ := s.host.CreateWorkspace(t.TempDir(), "demo")
	agent := func(kind, status string) {
		s.host.Update(c.PaneID, func(p *hosttest.Pane) { p.Proc = host.Process{Name: kind} })
		if kind == "claude" {
			s.record(c.PaneID, "s1", status, 0)
		} else {
			os.Remove(agentFile(filepath.Join(s.home, "agents"), "s1"))
		}
	}
	claude := t.TempDir()
	s.env["CLAUDE_CONFIG_DIR"] = claude
	os.MkdirAll(filepath.Join(claude, "projects", "-repo"), 0o755)
	os.WriteFile(filepath.Join(claude, "projects", "-repo", "s1.jsonl"), []byte(
		`{"type":"user","message":{"content":"go"}}`+"\n"+
			`{"type":"assistant","message":{"content":[{"type":"text","text":"looking"},{"type":"tool_use","id":"t1","name":"Bash","input":{}}]}}`+"\n"+
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`+"\n"+
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Done: all green.\n"}]}}`+"\n"), 0o644)
	agent("claude", "idle")
	out, errs, code := s.sh("", "last", c.PaneID)
	eq(t, "last message", fmt.Sprint(code, out, errs), "0Done: all green.\n")
	agent("claude", "working")
	_, errs, _ = s.sh("", "last", c.PaneID)
	eq(t, "working says so", strings.Contains(errs, "still working"), true)
	agent("vim", "")
	_, errs, code = s.sh("", "last", c.PaneID)
	eq(t, "not claude", fmt.Sprint(code, " ", strings.Contains(errs, "not a Claude Agent")), "1 true")
}
