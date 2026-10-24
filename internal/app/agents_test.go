package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"berthier/internal/host"
	"berthier/internal/host/hosttest"
)

// snapAgents is the Agents of the Host's snapshot, as "pane:status:session".
func snapAgents(t *testing.T, e Env) []string {
	t.Helper()
	snap, err := e.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, a := range snap.Agents {
		out = append(out, a.PaneID+":"+a.Status+":"+*a.Session)
	}
	return out
}

// `berthier hook` records a session's state for the pane it runs in; the
// snapshot shows the Agent until the session ends or its process is gone.
func TestHookStateReachesTheSnapshot(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "takes")
	s.host.Update(pane, func(p *hosttest.Pane) { p.Proc = host.Process{Name: "claude"} })
	s.env["FAKE_HOST_PANE"] = pane
	hook := func(event, payload string) {
		t.Helper()
		if out, errs, code := s.sh(payload, "hook", event); code != 0 || out != "" || errs != "" {
			t.Fatalf("hook %s: %d %q %q", event, code, out, errs)
		}
	}
	e := s.load()
	eq(t, "no hook yet: no Agent", snapAgents(t, e), "[]")

	hook("start", `{"session":"s1"}`)
	eq(t, "started is idle", snapAgents(t, e), "["+pane+":idle:s1]")
	hook("submit", `{"session":"s1"}`)
	eq(t, "working", snapAgents(t, e), "["+pane+":working:s1]")
	hook("permission", `{"session":"s1"}`)
	eq(t, "blocked", snapAgents(t, e), "["+pane+":blocked:s1]")
	hook("note", `{"session":"s1"}`)
	eq(t, "a note changes nothing", snapAgents(t, e), "["+pane+":blocked:s1]")
	hook("stop", `{"session":"s1"}`)
	eq(t, "idle", snapAgents(t, e), "["+pane+":idle:s1]")

	// A newer session in the pane (a /clear) takes over.
	hook("start", `{"session":"s2"}`)
	eq(t, "the newest session", snapAgents(t, e), "["+pane+":idle:s2]")

	// A crash writes no SessionEnd: the process is gone, back at the shell.
	s.host.Update(pane, func(p *hosttest.Pane) { p.Proc = host.Process{Name: "sh", AtShell: true} })
	eq(t, "a crash leaves no Agent", snapAgents(t, e), "[]")

	// A session that ends says why.
	s.host.Update(pane, func(p *hosttest.Pane) { p.Proc = host.Process{Name: "claude"} })
	hook("end", `{"session":"s2","reason":"exit"}`)
	eq(t, "an ended session leaves no Agent", snapAgents(t, e), "[]")
	if r := e.exitOf("s2"); r == nil || !e.harness().DeliberateExit(r.Reason) {
		t.Errorf("exit record %+v", r)
	}

	// Nothing outside the Host, nothing for what is not a hook.
	delete(s.env, "FAKE_HOST_PANE")
	hook("start", `{"session":"s3"}`)
	hook("no-such-event", `{"session":"s3"}`)
	hook("start", `not json`)
	if _, err := os.Stat(agentFile(e.Agents, "s3")); err == nil {
		t.Error("a hook outside the Host left a record")
	}
}

// An old record of a closed pane goes; a launch forgets what the pane's sessions said.
func TestAgentRecordsFollowTheirPane(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "takes")
	s.host.Update(pane, func(p *hosttest.Pane) { p.Proc = host.Process{Name: "claude"} })
	s.record("w9:p9", "gone", "idle", 0)
	old := AgentRec{Session: "ancient", Pane: "w9:p8", State: "idle", At: time.Now().Add(-time.Hour).Unix()}
	writeJSONAtomic(filepath.Join(s.home, "agents", "ancient.json"), old, false)
	s.record(pane, "old", "idle", 0)
	e := s.load()
	snapAgents(t, e)
	if _, err := os.Stat(agentFile(e.Agents, "gone")); err != nil {
		t.Error("a record of a pane that does not exist went at once: the Host may only list it late")
	}
	if _, err := os.Stat(agentFile(e.Agents, "ancient")); err == nil {
		t.Error("the old record of a pane that does not exist stayed")
	}
	s.host.Update(pane, func(p *hosttest.Pane) { p.Proc = host.Process{Name: "sh", AtShell: true} })
	if _, err := e.launch(pane, "demo", e.launchOpts("demo", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.Agents, "old.json")); err == nil {
		t.Error("a launch kept the pane's earlier session")
	}
}

// A process that says nothing after its start is blocked at startup.
func TestLaunchSilentIsBlocked(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "takes")
	s.host.OnStart = func(pane string, argv []string) {
		s.host.Update(pane, func(p *hosttest.Pane) {
			p.Proc = host.Process{Name: "claude"}
			p.Lines = []string{"Do you trust the files in this folder?"}
		})
	}
	s.env["BERTHIER_START_TIMEOUT_MS"] = "50"
	e := s.load()
	st, err := e.launch(pane, "demo", e.launchOpts("demo", ""))
	if err != nil || st != "blocked" {
		t.Fatalf("launch = %q, %v", st, err)
	}
	if ev := events(s, "start_blocked"); len(ev) != 1 || jq0(ev[0], "screen") == "" {
		t.Errorf("start_blocked events %v", ev)
	}
}

// A session terminated for a reason nobody chose (its Host restarted) is
// listed as unknown for a while: the Host brings it back, and a Revive would
// type into it. A crash, or a deliberate exit, leaves no Agent.
func TestTerminatedSessionHasGrace(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "takes")
	s.host.Update(pane, func(p *hosttest.Pane) { p.Proc = host.Process{Name: "sh", AtShell: true} })
	s.write("state.json", `{"updated_at":1,"agents":{"`+pane+`":{"kind":"claude","status":"idle","session_id":"s1"}}}`)
	e := s.load()
	s.recordExit(pane, "s1", "exited", "SessionEnd", "terminated")
	eq(t, "in grace", snapAgents(t, e), "["+pane+":unknown:s1]")
	e.ExitGrace = 0
	e.Host = e.wrapHost(s.host, e.harness())
	time.Sleep(1100 * time.Millisecond)
	eq(t, "past grace", snapAgents(t, e), "[]")
}

// While a Host restart is on, a pane at a shell with a session that never ended
// is the Host about to bring it back, not a crash.
func TestRestartWindowHoldsASession(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "takes")
	s.host.Update(pane, func(p *hosttest.Pane) { p.Proc = host.Process{Name: "sh", AtShell: true} })
	s.write("state.json", `{"updated_at":1,"agents":{"`+pane+`":{"kind":"claude","status":"idle","session_id":"s1"}}}`)
	e := s.load()
	s.record(pane, "s1", "working", 0)
	eq(t, "a crash", snapAgents(t, e), "[]")
	e.Restart.Store(time.Now().Add(time.Minute).Unix())
	eq(t, "a restart", snapAgents(t, e), "["+pane+":unknown:s1]")
	e.Restart.Store(0)
	eq(t, "restart over", snapAgents(t, e), "[]")
	s.write("state.json", `{"updated_at":1,"agents":{}}`)
	e.Restart.Store(time.Now().Add(time.Minute).Unix())
	eq(t, "a pane the Army gave up on stays given up on", snapAgents(t, e), "[]")
}

// What the Army knew of a pane before the hooks is not news of a launch: a
// start waits for the new session's own hook, or the prompt that follows would
// "take" on the change from the old state to the new.
func TestLaunchWaitsForItsOwnHook(t *testing.T) {
	s := newSandbox(t)
	pane := fakeHost(t, s, "takes")
	s.host.Update(pane, func(p *hosttest.Pane) { p.Proc = host.Process{Name: "claude"} })
	s.write("state.json", `{"updated_at":1,"agents":{"`+pane+`":{"kind":"claude","status":"idle","session_id":"old","state_seq":7}}}`)
	e := s.load()
	if a, _ := e.Host.Pane(pane); a.Agent == nil {
		t.Fatal("the Army forgot an Agent it had before the hooks")
	}
	if st, ok := e.announced(pane); ok {
		t.Errorf("announced = %q before any hook", st)
	}
	s.record(pane, "new", "started", 0)
	if st, ok := e.announced(pane); !ok || st != "idle" {
		t.Errorf("announced = %q, %v after SessionStart", st, ok)
	}
}
