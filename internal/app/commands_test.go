package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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
	dir := fakeHerdr(t, s)
	s.mode(dir, "takes")
	_, errs, code := s.sh("", "prompt", "p1", "go on")
	if code != 0 || errs != "" || calls(dir) != "pane read,agent prompt" {
		t.Errorf("code %d %q calls %q", code, errs, calls(dir))
	}
	text, _ := os.ReadFile(filepath.Join(dir, "text"))
	if string(text) != "go on" {
		t.Errorf("prompted %q", text)
	}
	s.mode(dir, "error")
	_, errs, code = s.sh("", "prompt", "p1", "hi")
	if code != 1 || !strings.Contains(errs, "agent target x not found") {
		t.Errorf("error case: %d %q", code, errs)
	}
	// Never into the user's draft: refused, nothing typed.
	s.mode(dir, "typing")
	os.Remove(filepath.Join(dir, "calls"))
	_, errs, code = s.sh("", "prompt", "p1", "hi")
	if code != 1 || !strings.Contains(errs, "input box holds the user's draft") || calls(dir) != "pane read" {
		t.Errorf("draft case: %d %q calls %q", code, errs, calls(dir))
	}
	_, errs, code = s.sh("", "prompt", "p1")
	if code != 1 || !strings.Contains(errs, "usage: berthier prompt") {
		t.Errorf("usage case: %d %q", code, errs)
	}
	s.mode(dir, "takes")
	_, errs, code = s.sh("line one\nline \"two\"\n", "prompt", "p1", "-")
	text, _ = os.ReadFile(filepath.Join(dir, "text"))
	if code != 0 || errs != "" || string(text) != "line one\nline \"two\"" {
		t.Errorf("stdin: %d %q prompted %q", code, errs, text)
	}
	_, errs, code = s.sh("\n", "prompt", "p1", "-")
	if code != 1 || !strings.Contains(errs, "usage: berthier prompt") {
		t.Errorf("empty stdin: %d %q", code, errs)
	}
}

func TestLastCommand(t *testing.T) {
	s := newSandbox(t)
	herdr := filepath.Join(t.TempDir(), "herdr")
	agent := func(kind, status string) {
		os.WriteFile(herdr, []byte(`#!/bin/sh
[ "$1 $2" = "agent get" ] && echo '{"result":{"agent":{"agent":"`+kind+`","agent_status":"`+status+`","agent_session":{"value":"s1"}}}}'
`), 0o755)
	}
	claude := t.TempDir()
	s.env["BERTHIER_HERDR"], s.env["CLAUDE_CONFIG_DIR"] = herdr, claude
	os.MkdirAll(filepath.Join(claude, "projects", "-repo"), 0o755)
	os.WriteFile(filepath.Join(claude, "projects", "-repo", "s1.jsonl"), []byte(
		`{"type":"user","message":{"content":"go"}}`+"\n"+
			`{"type":"assistant","message":{"content":[{"type":"text","text":"looking"},{"type":"tool_use","id":"t1","name":"Bash","input":{}}]}}`+"\n"+
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`+"\n"+
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Done: all green.\n"}]}}`+"\n"), 0o644)
	agent("claude", "idle")
	out, errs, code := s.sh("", "last", "p1")
	eq(t, "last message", fmt.Sprint(code, out, errs), "0Done: all green.\n")
	agent("claude", "working")
	_, errs, _ = s.sh("", "last", "p1")
	eq(t, "working says so", strings.Contains(errs, "still working"), true)
	agent("codex", "idle")
	_, errs, code = s.sh("", "last", "p1")
	eq(t, "not claude", fmt.Sprint(code, " ", strings.Contains(errs, "not a Claude Agent")), "1 true")
}
