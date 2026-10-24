package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"berthier/internal/agentsim"
	"berthier/internal/core"
	"berthier/internal/host/hosttest"
)

// fakeHost gives the sandbox a Host with one workspace, "demo", and an Agent
// simulator taking prompts the way mode says (agentsim). The workspace's pane
// is at a shell; Attach on s.sim puts an Agent in it. It returns the pane.
func fakeHost(t *testing.T, s *sandbox, mode string) (pane string) {
	t.Helper()
	s.host = hosttest.New()
	s.sim = agentsim.New(s.host, mode)
	s.sim.Record = s.record
	c, err := s.host.CreateWorkspace(t.TempDir(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	return c.PaneID
}

// hostCalls lists the methods the Host got, in order, but the reads of an
// Agent's state (Pane).
func hostCalls(s *sandbox) string {
	var l []string
	for _, c := range s.host.Calls() {
		if c.Method != "Pane" && c.Method != "Process" && c.Method != "CreateWorkspace" {
			l = append(l, c.Method)
		}
	}
	return strings.Join(l, ",")
}

// startArgs is the argv of the last Start the Host got, joined.
func startArgs(s *sandbox) string {
	c := s.host.CallsTo("Start")
	if len(c) == 0 {
		return ""
	}
	return strings.Join(c[len(c)-1].Args[1:], " ")
}

func TestStartClaude(t *testing.T) {
	brief := "Build it.\nThen report."
	start := func(mode, brief string) (got, warn, pane string, s *sandbox) {
		s = newSandbox(t)
		pane = fakeHost(t, s, mode)
		e := s.load()
		var warns []string
		got = e.StartClaude("demo", pane, brief, "demo", "", func(w string) { warns = append(warns, w) })
		return got, strings.Join(warns, "|"), pane, s
	}

	got, _, pane, s := start("takes", brief)
	eq(t, "brief sent as a prompt", got, "ready")
	eq(t, "brief tagged for the Profile", s.sim.Took(pane), "[berthier brief] Build it.\nThen report.")
	eq(t, "start has no brief argument", startArgs(s), "fake-agent --model opus")
	eq(t, "the Agent is named", s.host.CallsTo("RenamePane")[0].String(), "RenamePane "+pane+" demo")
	got, warn, _, _ := start("nostart", brief)
	eq(t, "start failure says why", got+" "+warn, "failed agent arguments cannot be encoded safely for the target shell")
	got, warn, pane, _ = start("never", brief)
	eq(t, "undelivered brief fails", got+" "+warn, "failed Brief not delivered: Claude in "+pane+" did not take the prompt")
	got, _, pane, s = start("takes", "")
	eq(t, "no brief, no prompt", got+" "+s.sim.Took(pane), "ready ")
}

// Behind a startup dialog the Agent is blocked: the Brief is not sent now.
func TestStartClaudeBehindDialog(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "dialog")
	e := s.load()
	if got := e.StartClaude("demo", pane, "", "demo", "", func(string) {}); got != "blocked" {
		t.Errorf("StartClaude behind a dialog = %q", got)
	}
}

func TestBriefAfterDialog(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "dialog")
	s.sim.Attach(pane)
	e := s.load()
	e.BriefWait = 5 * time.Second
	done := make(chan struct{})
	go func() { e.BriefAfterDialog("demo", pane, "Build it.\nThen report."); close(done) }()
	time.Sleep(20 * time.Millisecond)
	s.sim.Answer(pane)
	<-done
	line := strings.TrimSpace(s.read("loop.log"))
	eq(t, "brief sent after the dialog", jq(t, line, "kind")+jq(t, line, "pane")+jq(t, line, "name"), `"brief_sent"`+`"`+pane+`""demo"`)
	eq(t, "brief prompted", s.sim.Took(pane), "Build it.\nThen report.")

	// A dialog nobody answers: the Brief is given up on, and says so.
	s = newSandbox(t)
	pane = fakeHost(t, s, "dialog")
	s.sim.Attach(pane)
	e = s.load()
	e.BriefWait = 30 * time.Millisecond
	e.BriefAfterDialog("demo", pane, "Build it.")
	eq(t, "unanswered dialog", jq(t, strings.TrimSpace(s.read("loop.log")), "kind"), `"brief_failed"`)
}

func TestDivisionNewArgs(t *testing.T) {
	s := newSandbox(t)
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "GIT_") })
		if err := cmd.Run(); err != nil {
			t.Skip("no git")
		}
	}
	s.host = hosttest.New()
	s.sim = agentsim.New(s.host, "takes")
	s.sim.Record = s.record
	// The Corps's workspace, w1, labelled "corps", at the repo.
	if _, err := s.host.CreateWorkspace(repo, "corps"); err != nil {
		t.Fatal(err)
	}
	s.env["BERTHIER_WORKSPACE"] = "w1"

	run := func(args ...string) (string, int) {
		_, errs, code := s.sh("", append([]string{"division-new"}, args...)...)
		return errs, code
	}
	// A Division has no Artifact unless --artifact; its model is recorded.
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"model before brief", []string{"feat", "--model", "opus", "do it"}, "--model opus --deny Artifact"},
		{"model after brief", []string{"feat2", "do it", "--model", "opus"}, "--model opus --deny Artifact"},
		{"default model", []string{"feat3", "do it"}, "--model haiku --deny Artifact"},
		{"artifact", []string{"feat4", "--artifact", "do it"}, "--model haiku"},
	} {
		if errs, code := run(c.args...); code != 0 || !strings.HasSuffix(startArgs(s), c.want) {
			t.Errorf("%s: code %d %q, start %q, want suffix %q", c.name, code, errs, startArgs(s), c.want)
		}
		s.write("divisions.json", "{}")
	}
	// "-" reads the Brief from stdin, as prompt does; a blank Brief starts nothing.
	if _, errs, code := s.sh("Do this.\nThen that.\n", "division-new", "fromstdin", "-"); code != 0 {
		t.Errorf("brief from stdin: code %d %q", code, errs)
	} else if st := s.host.CallsTo("Start"); len(st) == 0 || s.sim.Took(st[len(st)-1].Args[0]) != "[berthier brief] Do this.\nThen that." {
		t.Errorf("brief from stdin was %q", s.sim.Took(st[len(st)-1].Args[0]))
	}
	starts := len(s.host.CallsTo("Start"))
	creates := len(s.host.CallsTo("CreateWorktree"))
	for _, c := range []struct{ stdin, brief string }{{"", ""}, {"", "  \t"}, {"", "-"}, {"\n  \n", "-"}} {
		_, errs, code := s.sh(c.stdin, "division-new", "blank", c.brief)
		if code != 1 || !strings.Contains(errs, "the Brief is empty") || len(s.host.CallsTo("Start")) != starts || len(s.host.CallsTo("CreateWorktree")) != creates {
			t.Errorf("blank Brief %q (stdin %q): code %d %q", c.brief, c.stdin, code, errs)
		}
	}
	for _, args := range [][]string{{"feat3", "do it", "extra"}, {"feat3", "--bogus", "do it"}, {"feat3", "do it", "--model"}, {"feat3"}, {"feat3", "--artifact"}} {
		errs, code := run(args...)
		if code != 1 || !strings.Contains(errs, "usage: berthier division-new") || len(s.host.CallsTo("Start")) != starts {
			t.Errorf("%v: code %d %q", args, code, errs)
		}
	}
}

// How Claude launches by workspace: a Division may not use Artifact unless it
// was spawned with --artifact.
func TestLaunchOpts(t *testing.T) {
	s := newSandbox(t)
	e := s.load()
	e.SetDivision("feat", &core.Division{Corps: "corps", Worktree: true, Model: "haiku"})
	e.SetDivision("art", &core.Division{Corps: "corps", Worktree: true, Artifact: true})
	e.SetDivision("adopted", &core.Division{Corps: "corps"})
	for _, c := range []struct{ label, model, want string }{
		{"corps", "", "--model opus"},
		{"corps", "sonnet", "--model sonnet"},
		{"corps", "opus", "--model opus"},
		{"berthier", "", "--model opus"},
		{"feat", "haiku", "--model haiku --disallow Artifact"},
		{"art", "haiku", "--model haiku"},
		{"adopted", "", "--disallow Artifact"},
		{".Feat", "", "--disallow Artifact"},
	} {
		o := e.launchOpts(c.label, c.model)
		got := []string{}
		if o.Model != "" {
			got = append(got, "--model", o.Model)
		}
		for _, d := range o.Disallow {
			got = append(got, "--disallow", d)
		}
		eq(t, c.label+" "+c.model, strings.Join(got, " "), c.want)
	}
}

func TestTranscriptLookup(t *testing.T) {
	s := newSandbox(t)
	s.env["CLAUDE_CONFIG_DIR"] = filepath.Join(s.home, "claude")
	e := s.load()
	os.MkdirAll(filepath.Join(e.ClaudeDir, "projects", "-x"), 0o755)
	os.WriteFile(filepath.Join(e.ClaudeDir, "projects", "-x", "s1.jsonl"), []byte(`{"type":"assistant","message":{"stop_reason":"tool_use","content":[]}}`+"\n"), 0o644)
	if !e.transcriptInterrupted("s1") || e.transcriptInterrupted("nope") {
		t.Error("transcript lookup")
	}
}
