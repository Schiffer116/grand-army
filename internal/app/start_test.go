package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"berthier/internal/core"
)

// fakeHerdr installs tests/fake-herdr, a Herdr with one Claude Agent whose
// `mode` says how it takes a prompt, and points the sandbox at it.
func fakeHerdr(t *testing.T, s *sandbox) (dir string) {
	src, err := os.ReadFile("../../tests/fake-herdr")
	if err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	os.WriteFile(filepath.Join(dir, "herdr"), src, 0o755)
	s.env["BERTHIER_HERDR"] = filepath.Join(dir, "herdr")
	return dir
}

func (s *sandbox) mode(dir, m string) {
	os.WriteFile(filepath.Join(dir, "mode"), []byte(m), 0o644)
	for _, f := range []string{"seq", "box", "calls", "text", "answered", "sid"} {
		os.Remove(filepath.Join(dir, f))
	}
}

func calls(dir string) string {
	raw, _ := os.ReadFile(filepath.Join(dir, "calls"))
	var l []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && !(f[0] == "agent" && f[1] == "get") {
			l = append(l, f[0]+" "+f[1])
		}
	}
	return strings.Join(l, ",")
}

func TestStartClaude(t *testing.T) {
	s := newSandbox(t)
	dir := fakeHerdr(t, s)
	e := LoadEnv(s.getenv)
	brief := "Build it.\nThen report."
	start := func(mode, brief string) (string, string) {
		s.mode(dir, mode)
		var warn []string
		got := e.StartClaude("demo", "p1", brief, "demo", "", func(w string) { warn = append(warn, w) })
		return got, strings.Join(warn, "|")
	}

	got, _ := start("takes", brief)
	eq(t, "brief sent as a prompt", got+" "+calls(dir), "ready agent start,pane read,agent prompt")
	text, _ := os.ReadFile(filepath.Join(dir, "text"))
	eq(t, "brief tagged for the Profile", string(text), "[berthier brief] Build it.\nThen report.")
	raw, _ := os.ReadFile(filepath.Join(dir, "calls"))
	eq(t, "start has no brief argument", strings.SplitN(string(raw), "\n", 2)[0], "agent start demo --kind claude --pane p1 --timeout 45000 -- --model opus")
	got, warn := start("nostart", brief)
	eq(t, "start failure says why", got+" "+warn, "failed agent arguments cannot be encoded safely for the target shell")
	got, warn = start("never", brief)
	eq(t, "undelivered brief fails", got+" "+warn, "failed Brief not delivered: Claude in p1 did not take the prompt")
}

func TestBriefAfterDialog(t *testing.T) {
	s := newSandbox(t)
	dir := fakeHerdr(t, s)
	e := LoadEnv(s.getenv)
	s.mode(dir, "dialog")
	e.BriefAfterDialog("demo", "p1", "Build it.\nThen report.")
	line := s.read("loop.log")
	eq(t, "brief sent after the dialog", jq(t, strings.TrimSpace(line), "kind")+jq(t, strings.TrimSpace(line), "pane")+jq(t, strings.TrimSpace(line), "name"), `"brief_sent""p1""demo"`)
	text, _ := os.ReadFile(filepath.Join(dir, "text"))
	eq(t, "brief prompted", string(text), "Build it.\nThen report.")
}

func TestDivisionNewArgs(t *testing.T) {
	s := newSandbox(t)
	dir := fakeHerdr(t, s)
	s.mode(dir, "takes")
	repo := t.TempDir()
	if err := exec.Command("git", "-C", repo, "init", "-q").Run(); err != nil {
		t.Skip("no git")
	}
	// A Herdr that knows the Corps's workspace, then falls back to fake-herdr.
	wrapper := filepath.Join(dir, "wrap")
	os.WriteFile(wrapper, []byte(`#!/bin/sh
case "$1 $2" in
  "workspace get") echo '{"result":{"workspace":{"label":"corps"}}}' ;;
  "workspace list") echo '{"result":{"workspaces":[]}}' ;;
  "pane list") echo '{"result":{"panes":[{"cwd":"`+repo+`"}]}}' ;;
  "worktree create") echo '{"result":{"root_pane":{"pane_id":"p1"}}}' ;;
  *) exec `+filepath.Join(dir, "herdr")+` "$@" ;;
esac
`), 0o755)
	s.env["BERTHIER_HERDR"] = wrapper
	s.env["BERTHIER_WORKSPACE"] = "w1"

	run := func(args ...string) (string, int) {
		os.Remove(filepath.Join(dir, "calls"))
		_, errs, code := s.sh("", append([]string{"division-new"}, args...)...)
		return errs, code
	}
	startArgs := func() string {
		raw, _ := os.ReadFile(filepath.Join(dir, "calls"))
		for _, l := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(l, "agent start") {
				return l
			}
		}
		return ""
	}
	// A Division has no Artifact unless --artifact; its model is recorded.
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"model before brief", []string{"feat", "--model", "opus", "do it"}, "-- --model opus --disallowedTools Artifact"},
		{"model after brief", []string{"feat2", "do it", "--model", "opus"}, "-- --model opus --disallowedTools Artifact"},
		{"default model", []string{"feat3", "do it"}, "-- --model haiku --disallowedTools Artifact"},
		{"artifact", []string{"feat4", "--artifact", "do it"}, "-- --model haiku"},
	} {
		if errs, code := run(c.args...); code != 0 || !strings.HasSuffix(startArgs(), c.want) {
			t.Errorf("%s: code %d %q, start %q, want suffix %q", c.name, code, errs, startArgs(), c.want)
		}
		s.write("divisions.json", "{}")
	}
	for _, args := range [][]string{{"feat3", "do it", "extra"}, {"feat3", "--bogus", "do it"}, {"feat3", "do it", "--model"}, {"feat3"}, {"feat3", "--artifact"}} {
		errs, code := run(args...)
		if code != 1 || !strings.Contains(errs, "usage: berthier division-new") || startArgs() != "" {
			t.Errorf("%v: code %d %q", args, code, errs)
		}
	}
}

// Claude's launch flags by workspace: a Division may not use Artifact unless
// it was spawned with --artifact.
func TestClaudeFlags(t *testing.T) {
	s := newSandbox(t)
	e := LoadEnv(s.getenv)
	e.SetDivision("feat", &core.Division{Corps: "corps", Worktree: true, Model: "haiku"})
	e.SetDivision("art", &core.Division{Corps: "corps", Worktree: true, Artifact: true})
	e.SetDivision("adopted", &core.Division{Corps: "corps"})
	for _, c := range []struct{ label, model, want string }{
		{"corps", "", "--model opus"},
		{"corps", "sonnet", "--model sonnet"},
		{"corps", "opus", "--model opus"},
		{"berthier", "", "--model opus"},
		{"feat", "haiku", "--model haiku --disallowedTools Artifact"},
		{"art", "haiku", "--model haiku"},
		{"adopted", "", "--disallowedTools Artifact"},
		{".Feat", "", "--disallowedTools Artifact"},
	} {
		eq(t, c.label+" "+c.model, strings.Join(e.claudeFlags(c.label, c.model), " "), c.want)
	}
}

func TestTranscriptLookup(t *testing.T) {
	s := newSandbox(t)
	s.env["CLAUDE_CONFIG_DIR"] = filepath.Join(s.home, "claude")
	e := LoadEnv(s.getenv)
	os.MkdirAll(filepath.Join(e.ClaudeDir, "projects", "-x"), 0o755)
	os.WriteFile(filepath.Join(e.ClaudeDir, "projects", "-x", "s1.jsonl"), []byte(`{"type":"assistant","message":{"stop_reason":"tool_use","content":[]}}`+"\n"), 0o644)
	if !e.transcriptInterrupted("s1") || e.transcriptInterrupted("nope") {
		t.Error("transcript lookup")
	}
}
