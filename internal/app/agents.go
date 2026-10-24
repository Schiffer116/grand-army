package app

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"berthier/internal/core"
	"berthier/internal/harness"
	"berthier/internal/host"
)

// AgentRec is state/agents/<session>.json: what the last hook of a session in
// a pane said (ADR 0006). The Host does not detect what an Agent is doing; the
// Harness's hooks do, through `berthier hook <event>`.
type AgentRec struct {
	Session string `json:"session"`
	Pane    string `json:"pane"`
	State   string `json:"state"` // a harness.State
	Event   string `json:"event"` // the harness's own name for the hook
	Reason  string `json:"reason,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	At      int64  `json:"at"`  // unix seconds
	Seq     int64  `json:"seq"` // unix nanoseconds: orders the records of a pane
}

func (r AgentRec) exited() bool { return r.State == string(harness.Exited) }

// agentFile is the record of session in dir.
func agentFile(dir, session string) string { return filepath.Join(dir, session+".json") }

// readAgentRecs lists every record in dir.
func readAgentRecs(dir string) []AgentRec {
	var out []AgentRec
	for _, f := range jsonFiles(dir) {
		var r AgentRec
		if readJSON(f, &r) && r.Session != "" && r.Pane != "" {
			out = append(out, r)
		}
	}
	return out
}

// newestByPane is each pane's latest record.
func newestByPane(recs []AgentRec) map[string]AgentRec {
	m := map[string]AgentRec{}
	for _, r := range recs {
		if old, ok := m[r.Pane]; !ok || r.Seq > old.Seq {
			m[r.Pane] = r
		}
	}
	return m
}

// recordHook saves a hook event as its session's record. A hook that changes
// nothing (the same state again, in the same pane) writes nothing.
func (e *Env) recordHook(h harness.HookEvent, pane string) error {
	var old AgentRec
	if readJSON(agentFile(e.Agents, h.Session), &old) && old.Pane == pane && old.State == string(h.State) && old.Reason == h.Reason {
		return nil
	}
	now := e.Now()
	return writeJSONAtomic(agentFile(e.Agents, h.Session), AgentRec{Session: h.Session, Pane: pane, State: string(h.State),
		Event: h.Event, Reason: h.Reason, Cwd: h.Cwd, At: now.Unix(), Seq: now.UnixNano()}, false)
}

// cmdHook is a Claude Code hook: `berthier hook <event>`, the payload as JSON
// on stdin. It records the session's state for the pane it runs in, and does
// nothing, silently and with status 0, outside the Host or for an event that
// changes no state: a hook must never get in Claude's way.
func cmdHook(e *Env, args []string, io IO) error {
	payload, _ := stdioReadAll(io.In)
	if len(args) != 1 {
		return nil
	}
	h, err := e.harness().Hook(args[0], payload)
	pane := e.selfPane()
	if err != nil || h.State == "" || h.Session == "" || pane == "" {
		return nil
	}
	e.recordHook(h, pane)
	return nil
}

// agentRecOf is the latest record of a pane, nil when no session ever said.
func agentRecOf(dir, pane string) *AgentRec {
	r, ok := newestByPane(readAgentRecs(dir))[pane]
	if !ok {
		return nil
	}
	return &r
}

// exitOf is the record of a session that ended, nil for any other.
func (e *Env) exitOf(session string) *AgentRec {
	var r AgentRec
	if readJSON(agentFile(e.Agents, session), &r) && r.exited() {
		return &r
	}
	return nil
}

// staleRecord is how long a record of a pane the Host does not list stays.
const staleRecord = 10 * time.Minute

// hooked is a Host whose panes carry the Agents the hooks report: a pane has
// an Agent while its latest session has not ended and its process still runs.
// A process that is gone (a crash writes no SessionEnd) leaves a pane with no
// Agent, which the Tick sees as a vanished one. The Host itself says nothing
// about Agents.
//
// A session that ended for a reason the user did not choose (Claude was told
// to terminate: its Host restarted) is given ExitGrace to start again before it
// counts as gone, listed meanwhile with the status "unknown": the Host brings
// such sessions back itself, and a Revive on top of that would type into a
// running Claude. So is a pane at a shell with a session that never ended,
// while a restart is on (Tick sets until after an outage, and a restarted
// Host kills its processes without a word).
type hooked struct {
	host.Host
	dir        string
	deliberate func(reason string) bool
	grace      time.Duration
	until      *atomic.Int64 // unix seconds: a restart is on until then
}

// agent is p with its Agent filled in from rec (nil: none), or false when the
// pane runs none.
func (h hooked) agent(p core.SnapAgent, rec *AgentRec) (core.SnapAgent, bool) {
	p.Agent, p.Status, p.Session, p.StateSeq = nil, "", nil, nil
	if rec == nil {
		return p, false
	}
	if rec.exited() {
		if rec.Reason == "" || h.deliberate(rec.Reason) || time.Since(time.Unix(rec.At, 0)) > h.grace {
			return p, false
		}
		kind, seq, sid := "claude", rec.Seq, rec.Session
		p.Agent, p.Status, p.Session, p.StateSeq = &kind, "unknown", &sid, &seq
		return p, true
	}
	if pr, err := h.Host.Process(p.PaneID); err == nil && pr.AtShell {
		if h.until == nil || time.Now().Unix() >= h.until.Load() {
			return p, false
		}
		kind, seq, sid := "claude", rec.Seq, rec.Session
		p.Agent, p.Status, p.Session, p.StateSeq = &kind, "unknown", &sid, &seq
		return p, true
	}
	kind, seq := "claude", rec.Seq
	sid, status := rec.Session, string(rec.State)
	if rec.State == string(harness.Started) {
		status = string(harness.Idle)
	}
	p.Agent, p.Status, p.Session, p.StateSeq = &kind, status, &sid, &seq
	return p, true
}

func (h hooked) Snapshot() (core.Snapshot, error) {
	s, err := h.Host.Snapshot()
	if err != nil {
		return s, err
	}
	recs := readAgentRecs(h.dir)
	newest := newestByPane(recs)
	here := map[string]bool{}
	s.Agents = nil
	for _, p := range s.Panes {
		here[p.PaneID] = true
		var rec *AgentRec
		if r, ok := newest[p.PaneID]; ok {
			rec = &r
		}
		if a, ok := h.agent(p, rec); ok {
			s.Agents = append(s.Agents, a)
		}
	}
	// A closed pane's records mean nothing, and its id may come back. (Not at
	// once: a restarted Host lists its panes late.)
	for _, r := range recs {
		if !here[r.Pane] && time.Since(time.Unix(r.At, 0)) > staleRecord {
			os.Remove(agentFile(h.dir, r.Session))
		}
	}
	return s, nil
}

func (h hooked) Panes(workspace string) ([]core.SnapAgent, error) {
	ps, err := h.Host.Panes(workspace)
	if err != nil {
		return nil, err
	}
	newest := newestByPane(readAgentRecs(h.dir))
	for i, p := range ps {
		if r, ok := newest[p.PaneID]; ok {
			ps[i], _ = h.agent(p, &r)
		}
	}
	return ps, nil
}

func (h hooked) Pane(id string) (core.SnapAgent, error) {
	p, err := h.Host.Pane(id)
	if err != nil {
		return p, err
	}
	p, _ = h.agent(p, agentRecOf(h.dir, id))
	return p, nil
}
