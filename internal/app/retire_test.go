package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"berthier/internal/agentsim"
	"berthier/internal/core"
	"berthier/internal/host/hosttest"
)

// gitIn runs git in dir with the user's own git environment out of the way.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "GIT_") })
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git %v: %v %s", args, err, out)
	}
}

func (s *sandbox) retired() map[string]retiredRec { e := s.load(); return e.ReadRetired() }

// retireSandbox is a Host with one Corps, "corps", whose workspace sits in a
// clean git repo that has a remote and everything pushed.
func retireSandbox(t *testing.T) (s *sandbox, repo string) {
	t.Helper()
	s = newSandbox(t)
	s.host = hosttest.New()
	s.sim = nil
	remote, repo := t.TempDir(), t.TempDir()
	gitIn(t, remote, "init", "-q", "--bare")
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	gitIn(t, repo, "remote", "add", "origin", remote)
	gitIn(t, repo, "push", "-q", "-u", "origin", "main")
	if _, err := s.host.CreateWorkspace(repo, "corps"); err != nil {
		t.Fatal(err)
	}
	s.write("state.json", `{"updated_at":1,"agents":{}}`)
	return s, repo
}

func TestCorpsRetireRefusals(t *testing.T) {
	s, repo := retireSandbox(t)
	retire := func() (string, int) {
		out, errs, code := s.sh("", "corps-retire", "corps")
		return out + errs, code
	}
	refused := func(name, want string) {
		t.Helper()
		got, code := retire()
		if code != 1 || !strings.Contains(got, want) {
			t.Errorf("%s: code %d %q, want a refusal naming %q", name, code, got, want)
		}
		if ws, _ := s.host.Workspaces(); len(ws) != 1 || len(s.host.CallsTo("CloseWorkspace")) != 0 {
			t.Errorf("%s: refused, but the workspace was touched: %v", name, ws)
		}
		if _, ok := s.retired()["corps"]; ok {
			t.Errorf("%s: refused, but marked retired", name)
		}
	}

	s.write("divisions.json", `{"feat":{"corps":"corps","worktree":true},"other":{"corps":"elsewhere"}}`)
	refused("open Division", "open Divisions: feat")
	s.write("divisions.json", `{}`)

	os.WriteFile(filepath.Join(repo, "wip.txt"), []byte("x"), 0o644)
	refused("dirty tree", "dirty tree")
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "wip")
	refused("unpushed commit", "1 unpushed commits")

	// Every reason at once.
	os.WriteFile(filepath.Join(repo, "more.txt"), []byte("x"), 0o644)
	s.write("divisions.json", `{"feat":{"corps":"corps"}}`)
	got, _ := retire()
	for _, want := range []string{"open Divisions: feat", "dirty tree", "unpushed commits"} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal %q does not say %q", got, want)
		}
	}

	for _, c := range []struct{ arg, want string }{{"nope", "no Corps 'nope'"}, {"berthier", "no Corps 'berthier'"}} {
		if _, errs, code := s.sh("", "corps-retire", c.arg); code != 1 || !strings.Contains(errs, c.want) {
			t.Errorf("retire %s: %d %q", c.arg, code, errs)
		}
	}
}

func TestCorpsRetireAndReopen(t *testing.T) {
	s, repo := retireSandbox(t)
	out, errs, code := s.sh("", "corps-retire", "Corps")
	if code != 0 || !strings.Contains(out, "retired") {
		t.Fatalf("retire: %d %q %q", code, out, errs)
	}
	if ws, _ := s.host.Workspaces(); len(ws) != 0 {
		t.Errorf("workspace still open: %v", ws)
	}
	rec := s.retired()["corps"]
	eq(t, "its dir is kept", rec.Dir, repo)
	if _, errs, code := s.sh("", "corps-retire", "corps"); code != 1 || !strings.Contains(errs, "already retired") {
		t.Errorf("second retire: %d %q", code, errs)
	}

	// Asks and notifies to it refuse, naming it retired.
	s.env["FAKE_HOST_PANE"] = "w9:p1"
	for _, args := range [][]string{{"ask", "corps", "hello"}, {"notify", "corps", "hello"}} {
		if _, errs, code := s.sh("", args...); code != 1 || !strings.Contains(errs, "Corps 'corps' is retired") || !strings.Contains(errs, "corps-reopen corps") {
			t.Errorf("%v: %d %q", args, code, errs)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(s.home, "inbox", "*.json")); len(m) != 0 {
		t.Errorf("a message to a retired Corps was queued: %v", m)
	}

	// Reopen: a workspace in its dir with a Main Agent started on a rebuild prompt.
	if _, errs, code := s.sh("", "corps-reopen", "nope"); code != 1 || !strings.Contains(errs, "not retired") {
		t.Errorf("reopen of a Corps that is not retired: %d %q", code, errs)
	}
	s.sim = agentsim.New(s.host, "takes")
	out, errs, code = s.sh("", "corps-reopen", "corps")
	creates := s.host.CallsTo("CreateWorkspace")
	if last := creates[len(creates)-1]; len(creates) != 2 || last.Args[0] != repo || last.Args[1] != "corps" {
		t.Errorf("reopen made %v", creates)
	}
	if code == 0 && !strings.Contains(out, "reopened") {
		t.Errorf("reopen said %q", out)
	}
	if _, ok := s.retired()["corps"]; ok {
		t.Errorf("still retired after a reopen (code %d, %q %q)", code, out, errs)
	}
	if starts := s.host.CallsTo("Start"); len(starts) != 1 {
		t.Errorf("no Main Agent started: %q", errs)
	} else if took := s.sim.Took(starts[0].Args[0]); !strings.HasPrefix(took, "[berthier fresh] ") || !strings.Contains(took, "berthier notes") {
		t.Errorf("Main Agent was told %q", took)
	}
}

// A retired Corps is invisible to the Main Loop: no Agent in the state, no
// Revive, and what waits for it stays in the inbox.
func TestRetiredCorpsIsLeftOutOfTheLoop(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "takes")
	s.sim.Attach(pane)
	s.write("retired.json", `{"demo":{"dir":"/x","at":1}}`)
	os.MkdirAll(filepath.Join(s.home, "inbox"), 0o755)
	s.write("inbox/1.json", `{"from":"x","to":"demo","text":"hi","at":1}`)
	e := s.load()
	for i := 0; i < 2; i++ {
		if err := e.Tick(); err != nil {
			t.Fatal(err)
		}
	}
	var st core.State
	readJSON(filepath.Join(s.home, "state.json"), &st)
	eq(t, "agents in the state", len(st.Agents.Keys)+len(st.Corps), 0)
	eq(t, "the message waits", len(e.ReadInbox()), 1)
	eq(t, "nothing prompted", s.sim.Took(pane), "")

	// Reopened (no longer listed), the Agent is seen again and gets its message.
	os.Remove(filepath.Join(s.home, "retired.json"))
	for i := 0; i < 3; i++ {
		e.Tick()
	}
	eq(t, "delivered once reopened", s.sim.Took(pane), "[berthier ask] from x: hi (Reply or report back with: berthier ask x \"...\")")
}

// state is a state.json with these Agents by workspace label: pane -> label:status.
func (s *sandbox) writeAgents(updatedAt int64, agents map[string]string) {
	var parts []string
	for pane, v := range agents {
		label, status, _ := strings.Cut(v, ":")
		parts = append(parts, `"`+pane+`":{"workspace_label":"`+label+`","status":"`+status+`"}`)
	}
	slices.Sort(parts)
	s.write("state.json", `{"updated_at":`+itoa(updatedAt)+`,"agents":{`+strings.Join(parts, ",")+`}}`)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func inboxTo(t *testing.T, s *sandbox, kind string) []string {
	t.Helper()
	var to []string
	m, _ := filepath.Glob(filepath.Join(s.home, "inbox", "*.json"))
	for _, f := range m {
		b, _ := os.ReadFile(f)
		if jq(t, string(b), "kind") == `"`+kind+`"` {
			to = append(to, strings.Trim(jq(t, string(b), "to"), `"`))
		}
	}
	slices.Sort(to)
	return to
}

func clearInbox(s *sandbox) {
	m, _ := filepath.Glob(filepath.Join(s.home, "inbox", "*.json"))
	for _, f := range m {
		os.Remove(f)
	}
}

// Shutdown snapshots who is working; Wake goes to them only, whatever they
// are doing by then, and says who it skipped.
func TestWakeWakesOnlyWhoWasWorkingAtShutdown(t *testing.T) {
	s := newSandbox(t)
	s.writeAgents(100, map[string]string{"%1": "busy:working", "%2": "calm:idle", "%3": "stuck:blocked", "%4": "done1:done"})
	out, _, code := s.sh("", "shutdown")
	eq(t, "shutdown goes to everyone", []any{code, inboxTo(t, s, "shutdown")}, []any{0, []string{"busy", "calm", "done1", "stuck"}})
	if !strings.Contains(out, "queued for 4 Agents") {
		t.Errorf("shutdown said %q", out)
	}
	clearInbox(s)

	// By the time of the Wake, the wrap-up turns are over: all idle.
	s.writeAgents(200, map[string]string{"%1": "busy:idle", "%2": "calm:idle", "%3": "stuck:idle", "%4": "done1:idle", "%5": "newer:idle"})
	out, _, code = s.sh("", "wake")
	eq(t, "wake goes to who was working", []any{code, inboxTo(t, s, "wake")}, []any{0, []string{"busy", "stuck"}})
	for _, want := range []string{"queued for 2 Agents", "woken (had work going, by shutdown): busy, stuck", "skipped (idle, left untouched): calm, done1, newer"} {
		if !strings.Contains(out, want) {
			t.Errorf("wake output %q lacks %q", out, want)
		}
	}
	clearInbox(s)

	// The snapshot answered one Wake: a second one goes by the state as it is.
	s.writeAgents(300, map[string]string{"%1": "busy:working", "%2": "calm:idle"})
	out, _, _ = s.sh("", "wake")
	eq(t, "no snapshot: the state's statuses", inboxTo(t, s, "wake"), []string{"busy"})
	if !strings.Contains(out, "by state") {
		t.Errorf("wake output %q", out)
	}
}

func TestWakeNamesWhoIsNotRunning(t *testing.T) {
	s := newSandbox(t)
	s.writeAgents(100, map[string]string{"%1": "busy:working", "%2": "gone1:working"})
	s.sh("", "shutdown")
	clearInbox(s)
	s.writeAgents(200, map[string]string{"%1": "busy:idle"})
	out, _, _ := s.sh("", "wake")
	eq(t, "woken", inboxTo(t, s, "wake"), []string{"busy"})
	if !strings.Contains(out, "not running (had work going): gone1") {
		t.Errorf("wake output %q", out)
	}
}

// Two Shutdowns in a row (the second after the wrap-up turns ended) keep the
// first one's snapshot.
func TestSecondShutdownKeepsTheSnapshot(t *testing.T) {
	s := newSandbox(t)
	s.writeAgents(100, map[string]string{"%1": "busy:working", "%2": "calm:idle"})
	s.sh("", "shutdown")
	s.writeAgents(150, map[string]string{"%1": "busy:idle", "%2": "calm:idle"})
	s.sh("", "shutdown")
	clearInbox(s)
	s.sh("", "wake")
	eq(t, "wake", inboxTo(t, s, "wake"), []string{"busy"})
}

// No Shutdown (a crash): a restart gap snapshots the statuses from before it,
// which the loop's first tick afterwards would otherwise overwrite.
func TestWakeAfterACrashUsesTheStatusesBeforeTheRestart(t *testing.T) {
	s := newSandbox(t)
	s.writeAgents(100, map[string]string{"%1": "busy:working", "%2": "calm:idle"})
	e := s.load()
	e.noteRestart(e.ReadState(), 100+e.RestartGap-1)
	if _, ok := e.readWakeSnap(); ok {
		t.Error("a tick on time snapshotted")
	}
	e.noteRestart(e.ReadState(), 100+e.RestartGap)
	// The loop came back and found everything idle.
	s.writeAgents(100+e.RestartGap, map[string]string{"%1": "busy:idle", "%2": "calm:idle"})
	s.sh("", "wake")
	eq(t, "wake", inboxTo(t, s, "wake"), []string{"busy"})

	// A Shutdown's snapshot is not replaced by the gap that follows it.
	s.writeAgents(1000, map[string]string{"%1": "busy:working", "%2": "calm:idle"})
	s.sh("", "shutdown")
	s.writeAgents(1001, map[string]string{"%1": "busy:idle", "%2": "calm:idle"})
	e.noteRestart(e.ReadState(), 1001+3600)
	clearInbox(s)
	s.sh("", "wake")
	eq(t, "wake after shutdown and a gap", inboxTo(t, s, "wake"), []string{"busy"})
}
