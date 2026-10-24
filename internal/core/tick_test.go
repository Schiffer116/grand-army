package core

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var testCfg = Config{BerthierRoot: "/home/u/berthier", BlockedAfter: 120, LongAfter: 3600, DivisionSettle: 30,
	ReviveMax: 3, ReviveBackoff: 60, ReviveReset: 300, EscalationTTL: 604800, SpeakAfter: 30, AskStuck: 1800,
	PingpongMax: 10, LimitGrace: 60, LimitRetry: 1800, BudgetWarn: 80,
	RestartGap: 1000000, RestartWait: 3600, RestartSettle: 20, CacheWarm: 3300, FrozenMin: 50000}

// js normalizes any value to what its JSON decodes to, for comparing with want.
func js(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func check(t *testing.T, name string, got any, want string) {
	t.Helper()
	var w any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("%s: bad want %q: %v", name, want, err)
	}
	if g := js(t, got); !reflect.DeepEqual(g, w) {
		gb, _ := json.Marshal(g)
		t.Errorf("%s\n  got:  %s\n  want: %s", name, gb, want)
	}
}

func ws(id, label, key, path string) Workspace {
	w := Workspace{ID: id, Label: label}
	if key != "" {
		w.Worktree = &Worktree{ptr(strings.TrimSuffix(key, "/.git")), ptr(path)}
	}
	return w
}

func wsn(w Workspace, n int) Workspace { w.Number = n; return w }

// ag is an agent of a snapshot: pane "w1:p1" belongs to workspace "w1".
func ag(pane, status, sid, name, cwd string) SnapAgent {
	if cwd == "" {
		cwd = "/x"
	}
	wsid := strings.Split(pane, ":")[0]
	a := SnapAgent{PaneID: pane, WorkspaceID: wsid, TabID: ptr(wsid + ":t1"), Agent: ptr("claude"), Status: status,
		Cwd: ptr(cwd), Title: ptr("t"), Name: nilIfEmpty(name)}
	if sid != "" {
		a.Session = ptr(sid)
	}
	return a
}

func seq(a SnapAgent, n int64) SnapAgent { a.StateSeq = &n; return a }

// held is a pane that holds a session with no agent listed (Herdr resuming it).
func held(pane, sid string) SnapAgent {
	a := ag(pane, "unknown", sid, "", "")
	a.Agent = nil
	return a
}

func pn(p string) SnapAgent { return SnapAgent{PaneID: p} }

func snap(w []Workspace, a, p []SnapAgent) Snapshot {
	return Snapshot{Workspaces: w, Agents: a, Panes: p}
}

func panes(l ...SnapAgent) []SnapAgent { return l }

type opt struct {
	inbox   []Ask
	divs    Divisions
	obs     map[string]Obs
	usage   Usage
	budgets map[string]float64
	gap     int64 // restart gap; default: never
	spend   SpendBrief
	quiet   int64 // draft quiet period
	thaw    []Thaw
}

var (
	W  = []Workspace{ws("w1", "skipli", "/r/skipli/.git", "/r/skipli"), ws("w2", "dark-mode", "/r/skipli/.git", "/h/dark-mode"), ws("w3", "berthier", "", ""), ws("w4", "notes", "", "")}
	D0 = Divisions{"dark-mode": {Corps: "skipli", Worktree: true}}
)

func tick(prev State, s Snapshot, now int64, o opt) TickOut {
	if o.divs == nil {
		o.divs = D0
	}
	cfg := testCfg
	if o.gap != 0 {
		cfg.RestartGap = o.gap
	}
	if o.quiet != 0 {
		cfg.DraftQuiet = o.quiet
	}
	// Like the Main Loop, read every input box a prompt waits on: empty unless
	// the test's obs says otherwise.
	obs := map[string]Obs{}
	for k, v := range o.obs {
		obs[k] = v
	}
	for {
		out := Tick(TickIn{Prev: prev, Snap: s, Now: now, Cfg: cfg, Divisions: o.divs, Inbox: o.inbox, Usage: o.usage, Budgets: o.budgets, Obs: obs, Spend: o.spend, Thaw: o.thaw})
		more := false
		// ... and every transcript a resume waits on: warm unless the test's obs says otherwise.
		for _, l := range out.Look {
			if v := obs[l.PaneID]; slices.Contains(l.Want, "draft") && v.Draft == nil {
				v.Draft, more = yes(false), true
				obs[l.PaneID] = v
			}
			if v := obs[l.PaneID]; slices.Contains(l.Want, "auto_fresh") && v.Auto == nil {
				v.Auto, more = &AutoCheck{}, true // no Brief kept unless the test says otherwise
				obs[l.PaneID] = v
			}
			if v := obs[l.PaneID]; len(l.Want) == 1 && l.Want[0] == "cache" && v.Cache == nil {
				v.Cache, more = &Cache{LastAt: now, Context: 1000}, true
				obs[l.PaneID] = v
			}
		}
		if !more {
			return out
		}
	}
}

func yes(b bool) *bool { return &b }

func escKeys(l []Escalation) []string {
	k := []string{}
	for _, e := range l {
		k = append(k, e.Key)
	}
	return k
}

func TestFirstObservation(t *testing.T) {
	s1 := snap(W, panes(ag("w1:p1", "idle", "sid-1", "", "")), panes(pn("w1:p1")))
	o := tick(State{}, s1, 1000, opt{})
	a := o.State.Agents.M["w1:p1"]
	check(t, "first run status", []any{a.Status, a.StatusSince, a.SessionID}, `["idle",1000,"sid-1"]`)
	check(t, "first run no actions", []int{len(o.Revive), len(o.Escalate), len(o.Notify)}, `[0,0,0]`)
}

func TestVanishedAndRevive(t *testing.T) {
	s1 := snap(W, panes(ag("w1:p1", "idle", "sid-1", "", "")), panes(pn("w1:p1")))
	prev := tick(State{}, s1, 1000, opt{}).State
	s2 := snap(W, nil, panes(pn("w1:p1")))

	o := tick(prev, s2, 1010, opt{})
	check(t, "vanished is looked at first", []any{o.Look, o.Revive}, `[[{"pane_id":"w1:p1","session_id":"sid-1","want":["exited","no_transcript","at_shell","cache"]}],[]]`)
	a := o.State.Agents.M["w1:p1"]
	check(t, "gone status", []any{a.Status, a.StatusSince, a.ReviveAttempts}, `["gone",1010,0]`)

	crashed := map[string]Obs{"w1:p1": {Exited: yes(false), AtShell: yes(true)}}
	o = tick(prev, s2, 1010, opt{obs: crashed})
	check(t, "revive action", []any{o.Look, o.Revive}, `[[],[{"pane_id":"w1:p1","name":"a-w1-p1","session_id":"sid-1","ev":{"pane":"w1:p1","corps":"skipli","name":"a-w1-p1","session":"sid-1"}}]]`)
	a = o.State.Agents.M["w1:p1"]
	check(t, "revive counted", []any{a.ReviveAttempts, a.LastReviveAt}, `[1,1010]`)

	o = tick(prev, s2, 1010, opt{obs: map[string]Obs{"w1:p1": {Exited: yes(false), AtShell: yes(false)}}})
	evs := []any{}
	for _, e := range o.Events {
		evs = append(evs, []any{e.Kind, e.Fields["pane"]})
	}
	check(t, "revive skipped off shell", []any{o.Revive, evs, o.State.Agents.M["w1:p1"].ReviveAttempts}, `[[],[["revive_skipped","w1:p1"]],1]`)

	o = tick(prev, s2, 1010, opt{obs: map[string]Obs{"w1:p1": {Exited: yes(true), AtShell: yes(true)}}})
	check(t, "deliberate exit forgotten", []any{o.Revive, o.Events, len(o.State.Agents.M), o.State.Corps[0].Agents}, `[[],[{"Kind":"forgot","Msg":"forgot a-w1-p1 in w1:p1: user exited it (session sid-1)","Fields":{"pane":"w1:p1","corps":"skipli","name":"a-w1-p1","session":"sid-1"}}],0,[]]`)

	o = tick(prev, s2, 1010, opt{obs: map[string]Obs{"w1:p1": {Exited: yes(false), NoTranscript: yes(true), AtShell: yes(true)}}})
	check(t, "no transcript forgotten, not revived", []any{o.Revive, o.Events, len(o.State.Agents.M)}, `[[],[{"Kind":"forgot","Msg":"forgot a-w1-p1 in w1:p1: its session has no transcript to resume (session sid-1)","Fields":{"pane":"w1:p1","corps":"skipli","name":"a-w1-p1","session":"sid-1"}}],0]`)

	// Backoff: a recent attempt suppresses another, and its pane is not read.
	prev2 := tick(prev, s2, 1010, opt{obs: crashed}).State
	o = tick(prev2, s2, 1030, opt{})
	check(t, "revive backoff", []any{o.Revive, o.Look}, `[[],[]]`)
	o = tick(prev2, s2, 1071, opt{})
	check(t, "revive after backoff looks again", o.Look[0].Want, `["exited","no_transcript","at_shell","cache"]`)
	if n := len(tick(prev2, s2, 1071, opt{obs: crashed}).Revive); n != 1 {
		t.Errorf("revive after backoff: %d", n)
	}

	// Max attempts: escalate once, never revive again.
	exh := prev2
	exh.Agents = cloneAgents(prev2.Agents)
	exh.Agents.M["w1:p1"].ReviveAttempts, exh.Agents.M["w1:p1"].LastReviveAt = 3, ptr(int64(1000))
	o = tick(exh, s2, 2000, opt{})
	check(t, "revive exhausted", []any{len(o.Revive), len(o.Look), escKeys(o.Escalate)}, `[0,0,["revive_failed:w1:p1:1010"]]`)
	texts := []string{}
	for _, s := range o.Speak {
		texts = append(texts, s.Text)
	}
	check(t, "revive_failed speech", texts, `["skipli crashed and could not be resumed"]`)
	o = tick(o.State, s2, 2010, opt{})
	check(t, "revive_failed spoken once, escalates once", []any{o.Speak, len(o.Escalate)}, `[[],0]`)
	// The last try is not a failure until the next tick still finds it gone.
	two := prev2
	two.Agents = cloneAgents(prev2.Agents)
	two.Agents.M["w1:p1"].ReviveAttempts, two.Agents.M["w1:p1"].LastReviveAt = 2, ptr(int64(1000))
	o = tick(two, s2, 2000, opt{obs: crashed})
	check(t, "last try escalates nothing yet", []any{len(o.Revive), o.Escalate, o.State.Agents.M["w1:p1"].ReviveAttempts}, `[1,[],3]`)
}

func cloneAgents(a Agents) Agents {
	var c Agents
	for _, k := range a.Keys {
		v := *a.M[k]
		c.Set(k, &v)
	}
	return c
}

func TestHerdrRestart(t *testing.T) {
	prev := tick(State{}, snap(W, panes(ag("w1:p1", "idle", "sid-1", "principal", "")), panes(pn("w1:p1"))), 1000, opt{}).State
	o := tick(prev, snap(W, nil, panes(held("w1:p1", "sid-1"))), 1010, opt{})
	check(t, "restoring pane not revived", []int{len(o.Revive), len(o.Look)}, `[0,0]`)
	a := o.State.Agents.M["w1:p1"]
	check(t, "restoring pane kept", []any{a.Name, a.Kind, a.Status, a.SessionID}, `["principal","claude","unknown","sid-1"]`)
	o = tick(o.State, snap(W, panes(ag("w1:p1", "idle", "sid-1", "", "")), panes(pn("w1:p1"))), 1020, opt{})
	rn := []Rename{}
	for _, r := range o.Rename {
		r.Ev = nil
		rn = append(rn, r)
	}
	check(t, "lost name restored", rn, `[{"pane_id":"w1:p1","name":"principal","session_id":"sid-1","ev":null}]`)
	check(t, "lost name kept in state", o.State.Agents.M["w1:p1"].Name, `"principal"`)
	o = tick(prev, snap(W, panes(ag("w1:p1", "idle", "sid-1", "principal", "")), panes(pn("w1:p1"))), 1020, opt{})
	check(t, "named agent left alone", o.Rename, `[]`)
	o = tick(prev, snap(W, panes(ag("w1:p1", "idle", "sid-2", "", "")), panes(pn("w1:p1"))), 1020, opt{})
	check(t, "new session keeps no name", []any{o.Rename, o.State.Agents.M["w1:p1"].Name}, `[[],null]`)
	o = tick(prev, snap(W, panes(ag("w1:p1", "idle", "sid-1", "", ""), ag("w4:p1", "idle", "sid-4", "principal", "")), panes(pn("w1:p1"), pn("w4:p1"))), 1020, opt{})
	check(t, "taken name not restored", o.Rename, `[]`)
}

func TestForgotten(t *testing.T) {
	s1 := snap(W, panes(ag("w1:p1", "idle", "sid-1", "", "")), panes(pn("w1:p1")))
	prev := tick(State{}, s1, 1000, opt{}).State
	check(t, "closed pane forgotten", len(tick(prev, snap(W, nil, nil), 2000, opt{}).State.Agents.M), `0`)
	prev = tick(State{}, snap(W, panes(ag("w1:p1", "idle", "", "", "")), panes(pn("w1:p1"))), 1000, opt{}).State
	check(t, "no session forgotten", len(tick(prev, snap(W, nil, panes(pn("w1:p1"))), 1010, opt{}).State.Agents.M), `0`)
}

func TestBlocked(t *testing.T) {
	sb := snap(W, panes(ag("w1:p1", "blocked", "sid-1", "", ""), ag("w3:p1", "blocked", "sid-b", "", "/home/u/berthier")), panes(pn("w1:p1"), pn("w3:p1")))
	prev := tick(State{}, sb, 1000, opt{}).State
	o := tick(prev, sb, 1100, opt{})
	check(t, "blocked under threshold", []int{len(o.Escalate), len(o.Notify)}, `[0,0]`)
	o = tick(prev, sb, 1130, opt{})
	check(t, "blocked escalates", escKeys(o.Escalate), `["blocked:w1:p1:1000"]`)
	check(t, "berthier blocked notifies", escKeys(o.Notify), `["blocked:w3:p1:1000"]`)
	check(t, "escalation event", []any{o.Escalate[0].Ev, o.Notify[0].Ev["to"]}, `[{"pane":"w1:p1","corps":"skipli","name":"a-w1-p1","why":"blocked","reason":"blocked 2m on an approval or question","to":"berthier"},"you"]`)
	check(t, "blocked speaks once", tick(o.State, sb, 1140, opt{}).Speak, `[]`)
	sp := func(o TickOut) []string {
		l := []string{}
		for _, s := range o.Speak {
			l = append(l, s.Text)
		}
		return l
	}
	check(t, "blocked speech", sp(tick(prev, sb, 1030, opt{})), `["skipli is waiting on you","berthier is waiting on you"]`)
	st := snap(W, panes(ag("w2:p1", "blocked", "sid-t", "", "")), panes(pn("w2:p1")))
	check(t, "spawned division speech", sp(tick(tick(State{}, st, 1000, opt{}).State, st, 1030, opt{})), `["dark mode in skipli is waiting on you"]`)
	check(t, "no speech under threshold", tick(prev, sb, 1020, opt{}).Speak, `[]`)
	check(t, "berthier recorded", o.State.Berthier, `"w3:p1"`)
}

func TestAsks(t *testing.T) {
	sa := snap(W, panes(ag("w1:p1", "idle", "sid-1", "", ""), ag("w4:p1", "working", "sid-4", "", ""), ag("w3:p1", "idle", "sid-b", "", "/home/u/berthier")),
		panes(pn("w1:p1"), pn("w4:p1"), pn("w3:p1")))
	prev := tick(State{}, sa, 1000, opt{}).State
	inbox := []Ask{ask("2", "notes", "Skipli", "again", 950), ask("1", "notes", "skipli", "hi", 900), ask("3", "skipli", "notes", "x", 100), ask("4", "notes", "berthier", "who owns dns?", 990)}
	o := tick(prev, sa, 2000, opt{inbox: inbox})
	ids := [][]string{}
	for _, d := range o.Deliver {
		ids = append(ids, []string{d.ID, d.PaneID})
	}
	check(t, "ask delivery", ids, `[["1","w1:p1"],["4","w3:p1"]]`)
	check(t, "ask text", o.Deliver[0].Text, `"[berthier ask] from notes: hi (Reply or report back with: berthier ask notes \"...\")"`)
	check(t, "ask stuck escalates", escKeys(o.Escalate), `["ask_stuck:3"]`)
	to := []string{}
	for _, e := range o.State.AskLog {
		to = append(to, e.To)
	}
	check(t, "ask logged", to, `["skipli","berthier"]`)

	// A notify is delivered like an ask but one-way: no reply hint, never stuck, never in the ask log.
	n1 := Ask{ID: "n1", Kind: "notify", From: "service", To: "skipli", Text: "line1\nline2", At: 100}
	o = tick(prev, sa, 2000, opt{inbox: []Ask{n1}})
	texts := [][]any{}
	for _, d := range o.Deliver {
		texts = append(texts, []any{d.ID, d.Text})
	}
	check(t, "notify delivered one-way", texts, `[["n1","[berthier notify] from service: line1\nline2"]]`)
	check(t, "notify never stuck or logged", []any{escKeys(o.Escalate), o.State.AskLog}, `[[],[]]`)
	n2 := Ask{ID: "n2", Kind: "notify", From: "service", To: "nobody", Text: "x", At: 100}
	check(t, "undelivered notify never stuck", escKeys(tick(prev, sa, 2000, opt{inbox: []Ask{n2}}).Escalate), `[]`)

	b1 := Ask{ID: "b1", Kind: "shutdown", From: "service", To: "skipli", Text: "[berthier shutdown] wrap up", At: 100}
	o = tick(prev, sa, 2000, opt{inbox: []Ask{b1}})
	texts = nil
	for _, d := range o.Deliver {
		texts = append(texts, []any{d.ID, d.Text, d.Oneway})
	}
	check(t, "broadcast delivered as is, one-way", texts, `[["b1","[berthier shutdown] wrap up",true]]`)
	w1 := Ask{ID: "w1", Kind: "wake", From: "service", To: "skipli", Text: "[berthier wake] up", At: 100}
	o = tick(prev, sa, 2000, opt{inbox: []Ask{w1, b1}})
	texts = nil
	for _, d := range o.Deliver {
		texts = append(texts, []any{d.ID, d.Wake})
	}
	check(t, "only a wake clears first", texts, `[["w1",true]]`)
	check(t, "broadcast never stuck", escKeys(tick(prev, sa, 2000, opt{inbox: []Ask{{ID: "b2", Kind: "wake", From: "service", To: "nobody", Text: "x", At: 100}}}).Escalate), `[]`)

	withLog := func(l []AskLogEntry, d Divisions) TickOut {
		p := prev
		p.AskLog = l
		return tick(p, sa, 2000, opt{inbox: []Ask{}, divs: d})
	}
	var oneway []AskLogEntry
	for i := range 12 {
		oneway = append(oneway, AskLogEntry{"notes", "skipli", int64(1900 + i)})
	}
	check(t, "one-way updates are not pingpong", escKeys(withLog(oneway, nil).Escalate), `[]`)
	var flips []AskLogEntry
	for i := range 11 {
		f, tt := "skipli", "notes"
		if i%2 == 1 {
			f, tt = tt, f
		}
		flips = append(flips, AskLogEntry{f, tt, int64(1900 + i)})
	}
	o = withLog(flips, nil)
	pp := [][]any{}
	for _, e := range o.Escalate {
		pp = append(pp, []any{e.Text, e.Ev["ask_from"], e.Ev["ask_to"]})
	}
	check(t, "pingpong escalates", pp, `[["asks bouncing between notes and skipli: 10 back-and-forths in the last hour","notes","skipli"]]`)
	check(t, "corps and its own division are not pingpong", escKeys(withLog(flips, Divisions{"notes": {Corps: "Skipli", Worktree: true}}).Escalate), `[]`)
	o = withLog(flips, Divisions{"notes": {Corps: "learning", Worktree: true}, "skipli": {Corps: "learning", Worktree: true}})
	check(t, "sibling divisions still pingpong", len(o.Escalate), `1`)
}

func TestDraftHold(t *testing.T) {
	// skipli's Main Agent is idle with an ask waiting; a Division of it is blocked (escalates to it).
	sa := snap(W, panes(ag("w1:p1", "idle", "sid-1", "", ""), ag("w2:p1", "blocked", "sid-2", "", ""), ag("w3:p1", "idle", "sid-b", "", "/home/u/berthier")),
		panes(pn("w1:p1"), pn("w2:p1"), pn("w3:p1")))
	prev := tick(State{}, sa, 1000, opt{}).State
	inbox := []Ask{ask("1", "notes", "skipli", "hi", 900)}
	first := Tick(TickIn{Prev: prev, Snap: sa, Now: 1200, Cfg: testCfg, Divisions: D0, Inbox: inbox})
	check(t, "nothing typed before its box is read", []any{len(first.Deliver), len(first.Escalate), first.State.Escalated["blocked:w2:p1:1000"], first.Look[len(first.Look)-1]},
		`[0,0,0,{"pane_id":"w1:p1","session_id":null,"want":["draft"]}]`)

	typing := map[string]Obs{"w1:p1": {Draft: yes(true)}}
	o := tick(prev, sa, 1200, opt{inbox: inbox, obs: typing, quiet: 20})
	check(t, "held while the box holds a draft", []any{len(o.Deliver), len(o.Escalate), o.State.Escalated["blocked:w2:p1:1000"], *o.State.Agents.M["w1:p1"].DraftAt},
		`[0,0,0,1200]`)
	check(t, "hold logged", o.Events, `[{"Kind":"draft_held","Msg":"holding prompts to a-w1-p1 in w1:p1: its input box holds the user's draft","Fields":{"corps":"skipli","name":"a-w1-p1","pane":"w1:p1"}}]`)
	o = tick(o.State, sa, 1210, opt{inbox: inbox, obs: typing, quiet: 20})
	check(t, "a hold is logged once", []any{len(o.Deliver), len(o.Events), *o.State.Agents.M["w1:p1"].DraftAt}, `[0,0,1210]`)
	o = tick(o.State, sa, 1220, opt{inbox: inbox, quiet: 20})
	check(t, "an empty box waits out the quiet period", []any{len(o.Deliver), len(o.Escalate)}, `[0,0]`)
	o = tick(o.State, sa, 1230, opt{inbox: inbox, quiet: 20})
	check(t, "then the held prompts go", []any{len(o.Deliver), escKeys(o.Escalate)}, `[1,["blocked:w2:p1:1000"]]`)

	// Berthier's box is read too, for the asks and escalations it gets; a suggestion is no draft.
	sb := snap(W, panes(ag("w4:p1", "blocked", "sid-4", "", ""), ag("w3:p1", "idle", "sid-b", "", "/home/u/berthier")), panes(pn("w4:p1"), pn("w3:p1")))
	prev = tick(State{}, sb, 1000, opt{}).State
	o = tick(prev, sb, 1200, opt{obs: map[string]Obs{"w3:p1": {Draft: yes(true)}}})
	check(t, "escalation to Berthier held", []any{len(o.Escalate), o.State.Escalated["blocked:w4:p1:1000"]}, `[0,0]`)
	check(t, "and goes once the box is empty", escKeys(tick(o.State, sb, 1210, opt{}).Escalate), `["blocked:w4:p1:1000"]`)
}

func TestDivisions(t *testing.T) {
	WD := []Workspace{ws("w1", "learning", "", ""), ws("w6", "tailscale", "", ""), ws("w3", "berthier", "", "")}
	dv := Divisions{"tailscale": {Corps: "learning"}}
	sd := snap(WD, panes(ag("w1:p1", "idle", "sid-1", "", ""), ag("w6:p1", "blocked", "sid-6", "", ""), ag("w3:p1", "idle", "sid-b", "", "/home/u/berthier")),
		panes(pn("w1:p1"), pn("w6:p1"), pn("w3:p1")))
	prev := tick(State{}, sd, 1000, opt{divs: dv, inbox: []Ask{}}).State
	o := tick(prev, sd, 1130, opt{divs: dv, inbox: []Ask{}})
	type nd struct {
		Name      string
		Divisions []string
	}
	var cs []nd
	for _, c := range o.State.Corps {
		n := nd{c.Name, []string{}}
		for _, d := range c.Divisions {
			n.Divisions = append(n.Divisions, d.Name)
		}
		cs = append(cs, n)
	}
	check(t, "division nested", cs, `[{"Name":"learning","Divisions":["tailscale"]}]`)
	e := o.Escalate[0]
	check(t, "division escalates to its corps", []any{e.Key, e.To, e.Ev["to"], e.Ev["corps"]}, `["blocked:w6:p1:1000","w1:p1","learning","tailscale"]`)
	check(t, "division speech event", []any{o.Speak[0].Ev["why"], o.Speak[0].Ev["to"], o.Speak[0].Ev["corps"], o.Speak[0].Text}, `["blocked","you","tailscale","tailscale in learning is waiting on you"]`)
	names := []string{}
	for _, c := range tick(prev, sd, 1130, opt{divs: Divisions{}}).State.Corps {
		names = append(names, c.Name)
	}
	check(t, "orphan division stays a corps", names, `["learning","tailscale"]`)
	idle := snap(WD, panes(ag("w1:p1", "idle", "sid-1", "", ""), ag("w6:p1", "idle", "sid-6", "", "")), panes(pn("w1:p1"), pn("w6:p1")))
	o = tick(prev, idle, 1130, opt{divs: dv, inbox: []Ask{ask("9", "skipli", "tailscale", "t", 1100)}})
	check(t, "division addressable", o.Deliver[0].PaneID, `"w6:p1"`)
	p2 := cloneState(prev)
	p2.Agents.M["w6:p1"].Status, p2.Agents.M["w6:p1"].PrevStatus = "working", ptr("idle")
	check(t, "adopted division stop is not escalated", tick(p2, idle, 1130, opt{divs: dv, inbox: []Ask{}}).Escalate, `[]`)
}

func cloneState(s State) State { s.Agents = cloneAgents(s.Agents); return s }

func TestBerthierDivisions(t *testing.T) {
	WB := []Workspace{ws("w1", "skipli", "", ""), ws("w3", "berthier", "", ""), ws("w7", "console", "", "")}
	dv := Divisions{"console": {Corps: "berthier", Worktree: true}}
	sbd := snap(WB, panes(ag("w3:p1", "idle", "sid-b", "", "/home/u/berthier"), ag("w7:p1", "blocked", "sid-7", "", "")), panes(pn("w3:p1"), pn("w7:p1")))
	o := tick(tick(State{}, sbd, 1000, opt{divs: dv}).State, sbd, 1130, opt{divs: dv})
	cn, bd := []string{}, []string{}
	for _, c := range o.State.Corps {
		cn = append(cn, c.Name)
	}
	for _, d := range o.State.BerthierDivisions {
		bd = append(bd, d.Name)
	}
	check(t, "berthier divisions", []any{cn, bd}, `[["skipli"],["console"]]`)
	to := []*string{}
	for _, e := range o.Escalate {
		to = append(to, e.To)
	}
	check(t, "berthier division escalates to berthier", []any{to, len(o.Notify)}, `[["w3:p1"],0]`)
}

func TestLongAndDivisionStopped(t *testing.T) {
	sw := snap(W, panes(ag("w1:p1", "working", "sid-1", "", "")), panes(pn("w1:p1")))
	prev := tick(State{}, sw, 1000, opt{}).State
	check(t, "long working", escKeys(tick(prev, sw, 4600, opt{}).Escalate), `["long:w1:p1:1000"]`)

	sw = snap(W, panes(ag("w2:p1", "working", "sid-2", "", ""), ag("w1:p1", "working", "sid-1", "", "")), panes(pn("w2:p1"), pn("w1:p1")))
	si := snap(W, panes(ag("w2:p1", "idle", "sid-2", "", ""), ag("w1:p1", "idle", "sid-1", "", "")), panes(pn("w2:p1"), pn("w1:p1")))
	prev = tick(State{}, sw, 1000, opt{}).State
	prev = tick(prev, si, 1100, opt{}).State
	check(t, "division settle wait", len(tick(prev, si, 1110, opt{}).Escalate), `0`)
	o := tick(prev, si, 1131, opt{})
	check(t, "division stopped", []any{o.Escalate[0].Key, o.Escalate[0].To, len(o.Escalate)}, `["division_stopped:w2:p1:1100","w1:p1",1]`)
}

func TestUsageLimits(t *testing.T) {
	sw := snap(W, panes(seq(ag("w1:p1", "working", "sid-1", "skipli", ""), 1)), panes(pn("w1:p1")))
	si := snap(W, panes(seq(ag("w1:p1", "idle", "sid-1", "skipli", ""), 2)), panes(pn("w1:p1")))
	prev := tick(State{}, sw, 1000, opt{}).State
	check(t, "working is not read", tick(prev, sw, 1010, opt{}).Look, `[]`)
	o := tick(prev, si, 1100, opt{})
	check(t, "idle is read", []any{o.Look, o.State.Agents.M["w1:p1"].Limit}, `[[{"pane_id":"w1:p1","session_id":"sid-1","want":["limit_reset"]}],null]`)
	read := func(r *int64) map[string]Obs { return map[string]Obs{"w1:p1": {LimitSeen: true, LimitReset: r}} }
	t.Setenv("TZ", "UTC")
	o = tick(prev, si, 1100, opt{obs: read(ptr(int64(5000)))})
	e := o.Events[0]
	check(t, "limit stop recorded", []any{o.Look, len(o.Events), e.Kind, e.Fields["name"], e.Fields["until"], e.Msg, o.Resume, o.State.Agents.M["w1:p1"].Limit},
		`[[],1,"limit_stop","skipli",5000,"usage limit: skipli in w1:p1 stopped until 1970-01-01 01:23 UTC",[],{"episode":"2:1100","since":1100,"reset_at":5000}]`)
	limited := o.State
	check(t, "read once per episode", tick(limited, si, 1110, opt{}).Look, `[]`)
	o = tick(prev, si, 1100, opt{obs: read(nil)})
	check(t, "no limit stop recorded", []any{o.Events, o.State.Agents.M["w1:p1"].Limit.ResetAt}, `[[],null]`)
	check(t, "no resume before reset", tick(limited, si, 5050, opt{}).Resume, `[]`)
	o = tick(limited, si, 5060, opt{})
	check(t, "resume after reset", o.Resume, `[{"pane_id":"w1:p1","name":"skipli","reset_at":5000,"ev":{"pane":"w1:p1","corps":"skipli","name":"skipli"}}]`)
	resumed := o.State
	a := resumed.Agents.M["w1:p1"]
	check(t, "resume recorded", []any{a.Limit.Resumed, a.LimitResumedAt}, `[true,5060]`)
	o = tick(limited, si, 5060, opt{obs: map[string]Obs{"w1:p1": {Draft: yes(true)}}})
	check(t, "a draft holds the resume", []any{o.Resume, o.State.Agents.M["w1:p1"].Limit.Resumed}, `[[],false]`)
	check(t, "resume once per episode", tick(resumed, si, 5070, opt{}).Resume, `[]`)
	check(t, "resume on the tick it is read", len(tick(prev, si, 2000, opt{obs: read(ptr(int64(1000)))}).Resume), `1`)
	check(t, "working ends the episode", tick(limited, sw, 5070, opt{}).Resume, `[]`)
	// A turn between two ticks: same status, but Herdr bumped its sequence.
	si3 := snap(W, panes(seq(ag("w1:p1", "idle", "sid-1", "skipli", ""), 3)), panes(pn("w1:p1")))
	o = tick(resumed, si3, 5080, opt{})
	check(t, "turn between ticks is a new episode", []any{len(o.Look), o.Resume}, `[1,[]]`)
	// It stopped on the limit again right after the nudge: wait out limit_retry.
	again := tick(resumed, si3, 5080, opt{obs: read(ptr(int64(5000)))}).State
	check(t, "retry waits", tick(again, si3, 6000, opt{}).Resume, `[]`)
	check(t, "retry after limit_retry", len(tick(again, si3, 6860, opt{}).Resume), `1`)
	check(t, "gone is never resumed", tick(limited, snap(W, nil, panes(pn("w1:p1"))), 9000, opt{}).Resume, `[]`)
}

func TestCorpsAndOrder(t *testing.T) {
	si := snap(W, panes(seq(ag("w1:p1", "idle", "sid-1", "skipli", ""), 2)), panes(pn("w1:p1")))
	o := tick(State{}, si, 1000, opt{})
	type div struct {
		Name     string
		Worktree bool
		Path     *string
	}
	cs := []any{}
	for _, c := range o.State.Corps {
		ds := []div{}
		for _, d := range c.Divisions {
			ds = append(ds, div{d.Name, d.Worktree, d.Path})
		}
		cs = append(cs, map[string]any{"name": c.Name, "divisions": ds})
	}
	check(t, "corps", cs, `[{"name":"skipli","divisions":[{"Name":"dark-mode","Worktree":true,"Path":"/h/dark-mode"}]},{"name":"notes","divisions":[]}]`)

	Wh := []Workspace{ws("w1", "skipli", "", ""), ws("w5", "hq", "", "")}
	sh := snap(Wh, panes(ag("w5:p1", "idle", "sid-h", "", "/home/u/berthier/tests"), ag("w1:p1", "idle", "sid-1", "", "/home/u/berthier-other")), panes(pn("w5:p1"), pn("w1:p1")))
	o = tick(State{}, sh, 1000, opt{})
	names := []string{}
	for _, c := range o.State.Corps {
		names = append(names, c.Name)
	}
	check(t, "berthier by cwd", []any{o.State.Berthier, names}, `["w5:p1",["skipli"]]`)

	dv := Divisions{"dark-mode": {Corps: "skipli", Worktree: true}, "light-mode": {Corps: "skipli", Worktree: true}}
	sn := snap([]Workspace{wsn(ws("w4", "notes", "", ""), 4), wsn(ws("w2", "dark-mode", "", ""), 3), wsn(ws("w3", "berthier", "", ""), 1),
		wsn(ws("w5", "light-mode", "", ""), 5), wsn(ws("w1", "skipli", "", ""), 2)}, nil, nil)
	o = tick(State{}, sn, 1000, opt{divs: dv})
	check(t, "herdr order recorded", o.State.Order, `["w3","w1","w2","w4","w5"]`)
	cs = []any{}
	for _, c := range o.State.Corps {
		ds := []string{}
		for _, d := range c.Divisions {
			ds = append(ds, d.Name)
		}
		cs = append(cs, []any{c.Name, ds})
	}
	check(t, "corps in herdr order", cs, `[["skipli",["dark-mode","light-mode"]],["notes",[]]]`)
}

func used(s, d float64) Usage {
	return Usage{ResetsAt: ptr(9000.0), WeekPct: ptr(50.0), ByCorps: map[string]float64{"skipli": s, "dotfiles": d}}
}

func TestBudgets(t *testing.T) {
	budgets := map[string]float64{"skipli": 10, "dotfiles": 5}
	sb := snap(W, panes(ag("w1:p1", "idle", "sid-1", "", ""), ag("w2:p1", "idle", "sid-2", "", ""), ag("w4:p1", "idle", "sid-4", "", "")),
		panes(pn("w1:p1"), pn("w2:p1"), pn("w4:p1")))
	prev := tick(State{}, sb, 1000, opt{}).State
	o := func(p State, s Snapshot, now int64, u Usage) TickOut {
		return tick(p, s, now, opt{usage: u, budgets: budgets, inbox: []Ask{}})
	}
	r := o(prev, sb, 1010, used(7.9, 0))
	check(t, "under 80% is quiet", []any{r.Escalate, r.Speak, r.Events}, `[[],[],[]]`)
	r = o(prev, sb, 1010, used(8, 0))
	e := r.Escalate[0]
	check(t, "80% tells the main agent", []any{len(r.Escalate), e.Key, e.To, e.Head, e.Text, e.Ev},
		`[1,"budget_warn:skipli:9000","w1:p1","[berthier-loop] Weekly budget:","skipli has used 8% of the week, 80% of its 10% budget: wind down low-value work, since at 100% its Revives, usage-limit resumes and ask delivery are held",{"why":"budget_warn","reason":"80% of weekly budget","corps":"skipli","pct":80,"to":"skipli"}]`)
	check(t, "80% speaks", []any{r.Speak[0].Key, r.Speak[0].Text}, `["speak_budget_warn:skipli:9000","skipli has used 80 percent of its weekly budget"]`)
	p := prev
	p.Escalated = map[string]int64{"budget_warn:skipli:9000": 1010, "speak_budget_warn:skipli:9000": 1010}
	r = o(p, sb, 1020, used(8, 0))
	check(t, "warned once per week", []any{r.Escalate, r.Speak}, `[[],[]]`)
	u := used(8, 0)
	u.ResetsAt = ptr(99000.0)
	p.Escalated = map[string]int64{"budget_warn:skipli:9000": 1010}
	check(t, "next week warns again", escKeys(o(p, sb, 1020, u).Escalate), `["budget_warn:skipli:99000"]`)
	r = o(prev, snap(W, nil, nil), 1010, used(8, 0))
	check(t, "no main agent: berthier hears", []any{r.Escalate[0].Key, r.Escalate[0].To, r.Escalate[0].Ev["to"]}, `["budget_warn:skipli:9000",null,"berthier"]`)
	r = o(prev, sb, 1010, used(8, 0))
	check(t, "a division's use counts for its corps", r.Escalate[0].Ev["corps"], `"skipli"`)
	r = tick(prev, sb, 1010, opt{usage: used(8, 0), budgets: budgets, inbox: []Ask{}, spend: SpendBrief{Waste: []WasteItem{
		{Kind: "bloat", Corps: "skipli", Turn: "s:4", USD: 1.5}, {Kind: "reread", Corps: "notes", Turn: "n:1", USD: 1}, {Kind: "cache_miss", Corps: "skipli", Turn: "s:9", USD: 0.25}}}})
	check(t, "the warning carries the Corps's waste profile", r.Escalate[0].Text,
		`"skipli has used 8% of the week, 80% of its 10% budget: wind down low-value work, since at 100% its Revives, usage-limit resumes and ask delivery are held. Its top Waste signals this week: bloat $1.50 (s:4), cache_miss $0.25 (s:9); details: berthier profile --waste --corps skipli"`)
	u = used(20, 20)
	u.ResetsAt = ptr(1000.0)
	r = o(prev, sb, 1010, u)
	check(t, "expired week counts for nothing", []any{r.Escalate, r.Speak, r.Events, r.Revive}, `[[],[],[],[]]`)
	r = o(prev, sb, 1010, Usage{WeekPct: ptr(73.0)})
	check(t, "baseline-only usage is quiet", []any{r.Escalate, r.Speak, r.Events}, `[[],[],[]]`)
	r = tick(prev, sb, 1010, opt{usage: used(20, 20), budgets: map[string]float64{}, inbox: []Ask{}})
	check(t, "no budget set, no event", []any{r.Escalate, r.Speak, r.Events}, `[[],[],[]]`)

	// At 100%: logged and spoken once, no warning on top.
	r = o(prev, sb, 1010, used(10.5, 0))
	check(t, "100% logs once", []any{len(r.Events), r.Events[0].Kind, r.Events[0].Fields["corps"], r.Events[0].Msg},
		`[1,"budget_exhausted","skipli","skipli used 10.5% of the week, over its 10% budget: holding Revives, usage-limit resumes and ask delivery for it and its Divisions until the week resets"]`)
	sp := []string{}
	for _, s := range r.Speak {
		sp = append(sp, s.Text)
	}
	check(t, "100% speaks", []any{r.Escalate, sp}, `[[],["skipli is over its weekly budget"]]`)
	r2 := o(r.State, sb, 1020, used(10.5, 0))
	check(t, "100% is not repeated", []any{r2.Events, r2.Speak}, `[[],[]]`)
	keys := func(s State) []string {
		k := []string{}
		for x := range s.Escalated {
			k = append(k, x)
		}
		sortStrings(k)
		return k
	}
	check(t, "100% keys survive the next tick", keys(r.State), `["budget_block:skipli:9000","speak_budget_block:skipli:9000"]`)
	check(t, "block key kept for the week", keys(r2.State), `["budget_block:skipli:9000","speak_budget_block:skipli:9000"]`)

	// Revives are skipped for the Corps and its Divisions, not for others.
	gone := snap(W, nil, panes(pn("w1:p1"), pn("w2:p1"), pn("w4:p1")))
	crashed := map[string]Obs{}
	for _, p := range []string{"w1:p1", "w2:p1", "w4:p1"} {
		crashed[p] = Obs{Exited: yes(false), AtShell: yes(true)}
	}
	rv := func(u Usage) TickOut {
		return tick(prev, gone, 1010, opt{usage: u, budgets: budgets, obs: crashed, inbox: []Ask{}})
	}
	revs := func(o TickOut) []string {
		l := []string{}
		for _, v := range o.Revive {
			l = append(l, v.PaneID)
		}
		return l
	}
	check(t, "revive held at 100%", revs(rv(used(10.5, 0))), `["w4:p1"]`)
	check(t, "revive resumes under budget", revs(rv(used(9, 0))), `["w1:p1","w2:p1","w4:p1"]`)
	check(t, "held revive costs no attempt", rv(used(10.5, 0)).State.Agents.M["w1:p1"].ReviveAttempts, `0`)

	// Usage-limit resumes wait.
	swl := snap(W, panes(seq(ag("w1:p1", "working", "sid-1", "skipli", ""), 1), seq(ag("w2:p1", "working", "sid-2", "", ""), 1)), panes(pn("w1:p1"), pn("w2:p1")))
	sil := snap(W, panes(seq(ag("w1:p1", "idle", "sid-1", "skipli", ""), 2), seq(ag("w2:p1", "idle", "sid-2", "", ""), 2)), panes(pn("w1:p1"), pn("w2:p1")))
	pl := tick(State{}, swl, 1000, opt{}).State
	lim := map[string]Obs{"w1:p1": {LimitSeen: true, LimitReset: ptr(int64(1000))}, "w2:p1": {LimitSeen: true, LimitReset: ptr(int64(1000))}}
	check(t, "resume held at 100%", len(tick(pl, sil, 2000, opt{usage: used(10.5, 0), budgets: budgets, obs: lim}).Resume), `0`)
	check(t, "resume under budget", len(tick(pl, sil, 2000, opt{usage: used(1, 0), budgets: budgets, obs: lim}).Resume), `2`)

	// Ask delivery to the Corps and its Divisions waits; other Corps are unaffected.
	inbox := []Ask{ask("1", "notes", "skipli", "a", 900), ask("2", "notes", "dark-mode", "b", 901), ask("3", "skipli", "notes", "c", 902)}
	ids := func(o TickOut) []string {
		l := []string{}
		for _, d := range o.Deliver {
			l = append(l, d.ID)
		}
		return l
	}
	check(t, "asks held at 100%", ids(tick(prev, sb, 1010, opt{usage: used(10.5, 0), budgets: budgets, inbox: inbox})), `["3"]`)
	check(t, "asks delivered under budget", ids(tick(prev, sb, 1010, opt{usage: used(1, 0), budgets: budgets, inbox: inbox})), `["1","2","3"]`)
}

func sortStrings(l []string) { slices.Sort(l) }

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ask(id, from, to, text string, at int64) Ask {
	return Ask{ID: id, From: from, To: to, Text: text, At: at}
}

func TestRestartResume(t *testing.T) {
	// Agents working before a gap are remembered until each is back. A session
	// relisted idle is read for an unfinished turn, then prompted once.
	sw := snap(W, panes(ag("w1:p1", "working", "sid-1", "principal", ""), ag("w4:p1", "idle", "sid-4", "", ""), ag("w3:p1", "blocked", "sid-3", "", "")),
		panes(pn("w1:p1"), pn("w4:p1"), pn("w3:p1")))
	pw := tick(State{}, sw, 1000, opt{}).State
	empty := snap(W, nil, nil)
	g := opt{gap: 45}
	o := tick(pw, empty, 1010, g)
	check(t, "no gap, nothing remembered", o.State.Interrupted, `{}`)
	o = tick(pw, empty, 1100, g)
	check(t, "gap remembers only the working", o.State.Interrupted, `{"sid-1":{"name":"principal","since":1100}}`)
	o2 := tick(o.State, snap(W, nil, panes(held("w1:p1", "sid-1"))), 1110, g)
	keys := func(s State) []string {
		k := []string{}
		for x := range s.Interrupted {
			k = append(k, x)
		}
		sortStrings(k)
		return k
	}
	check(t, "still resuming: kept, no read", []any{keys(o2.State), o2.Look, o2.Proceed}, `[["sid-1"],[],[]]`)
	reads := func(o TickOut) []Look {
		l := []Look{}
		for _, x := range o.Look {
			if len(x.Want) == 1 && x.Want[0] == "interrupted" {
				l = append(l, x)
			}
		}
		return l
	}
	hi := held("w1:p1", "sid-1")
	hi.Status = "idle"
	o3 := tick(o2.State, snap(W, nil, panes(hi)), 1115, g)
	check(t, "held pane with a status is not prompted yet", []any{reads(o3), keys(o3.State)}, `[[],["sid-1"]]`)
	si := snap(W, panes(ag("w1:p1", "idle", "sid-1", "", "")), panes(pn("w1:p1")))
	o3 = tick(o2.State, si, 1120, g)
	check(t, "back idle: waits for the status to hold", []any{reads(o3), keys(o3.State)}, `[[],["sid-1"]]`)
	o3 = tick(o3.State, si, 1140, g)
	check(t, "back idle: transcript read", []any{reads(o3), keys(o3.State), o3.Proceed}, `[[{"pane_id":"w1:p1","session_id":"sid-1","want":["interrupted"]}],["sid-1"],[]]`)
	p3 := o3.State
	obsInt := func(v bool) map[string]Obs { return map[string]Obs{"w1:p1": {Interrupted: &v}} }
	o4 := tick(p3, si, 1140, opt{gap: 45, obs: obsInt(true)})
	check(t, "interrupted: prompted once", []any{o4.Proceed, o4.State.Interrupted}, `[[{"pane_id":"w1:p1","name":"principal","ev":{"pane":"w1:p1","corps":"skipli","name":"a-w1-p1","session":"sid-1"}}],{"sid-1":{"name":"principal","since":1100,"tries":1}}]`)
	// The nudge took: the Agent is working, or finished its turn; either way forgotten.
	ow := tick(o4.State, snap(W, panes(ag("w1:p1", "working", "sid-1", "", "")), panes(pn("w1:p1"))), 1150, g)
	check(t, "nudge took (working): forgotten", []any{ow.Proceed, ow.State.Interrupted}, `[[],{}]`)
	od := tick(o4.State, si, 1150, opt{gap: 45, obs: obsInt(false)})
	check(t, "nudge took (turn finished): forgotten", []any{od.Proceed, od.State.Interrupted}, `[[],{}]`)
	// The nudge did not take (Herdr listed the pane without its agent): retried
	// on later ticks, then given up on once, in the log.
	st, nudges, gave := o4.State, 1, 0
	for n := 1; n < 8; n++ {
		ox := tick(st, si, 1140+int64(10*n), opt{gap: 45, obs: obsInt(true)})
		nudges += len(ox.Proceed)
		for _, ev := range ox.Events {
			if ev.Kind == "continue_failed" {
				gave++
			}
		}
		st = ox.State
	}
	check(t, "failed nudge: 5 tries, one give-up, forgotten", []any{nudges, gave, st.Interrupted}, `[5,1,{}]`)
	o4 = tick(p3, si, 1140, opt{gap: 45, obs: obsInt(false)})
	check(t, "finished turn: forgotten", []any{o4.Proceed, o4.State.Interrupted}, `[[],{}]`)
	o4 = tick(o2.State, snap(W, panes(ag("w1:p1", "working", "sid-1", "", "")), panes(pn("w1:p1"))), 1120, g)
	check(t, "working again: forgotten", []any{o4.Look, o4.Proceed, o4.State.Interrupted}, `[[],[],{}]`)
	check(t, "never relisted: expires", tick(o.State, empty, 5000, g).State.Interrupted, `{}`)

	// A Corps over budget keeps its Agent waiting.
	r := tick(p3, si, 1140, opt{gap: 45, obs: obsInt(true), usage: used(1, 0), budgets: map[string]float64{"skipli": 1}})
	check(t, "over budget: held, kept", []any{r.Proceed, keys(r.State)}, `[[],["sid-1"]]`)
	// So does a draft in its input box.
	r = tick(p3, si, 1140, opt{gap: 45, obs: map[string]Obs{"w1:p1": {Interrupted: yes(true), Draft: yes(true)}}})
	check(t, "draft: held, kept", []any{r.Proceed, r.State.Interrupted}, `[[],{"sid-1":{"name":"principal","since":1100}}]`)
}

func TestWeeklyOptimizer(t *testing.T) {
	sb := snap(W, panes(ag("w3:p1", "idle", "sid-b", "", "/home/u/berthier")), panes(pn("w3:p1")))
	prev := tick(State{}, sb, 1000, opt{}).State
	week := Usage{ResetsAt: ptr(float64(Week) * 3)} // weeks start at multiples of Week
	start := int64(Week * 2)
	spend := SpendBrief{LastWeekUSD: 120.5, LastWeekWaste: 30.25}
	r := tick(prev, sb, start+3599, opt{usage: week, spend: spend})
	check(t, "not in the week's first hour", r.Escalate, `[]`)
	r = tick(prev, sb, start+3600, opt{usage: week, spend: spend})
	check(t, "Berthier asked to spawn an optimizer", []any{len(r.Escalate), r.Escalate[0].Key, r.Escalate[0].Head, r.Escalate[0].Ev},
		`[1,"optimize:1209600","[berthier-loop] Weekly Profile review:",{"why":"optimize","reason":"weekly Profile review","usd":120.5,"to":"berthier"}]`)
	check(t, "the ask", strings.Contains(r.Escalate[0].Text, "last week the Grand Army spent $120.50, $30.25 of it on Waste signals") &&
		strings.Contains(r.Escalate[0].Text, "as an Artifact for the user's approval; it never applies them itself"), `true`)
	r = tick(r.State, sb, start+7200, opt{usage: week, spend: spend})
	check(t, "once a week", r.Escalate, `[]`)
	r = tick(prev, sb, start+3600, opt{usage: week})
	check(t, "nothing spent last week, nothing to review", r.Escalate, `[]`)
}

func TestFrozen(t *testing.T) {
	berthier := ag("w3:p1", "idle", "sid-b", "", "/home/u/berthier")
	s1 := snap(W, panes(ag("w1:p1", "idle", "sid-1", "", ""), berthier), panes(pn("w1:p1"), pn("w3:p1")))
	prev := tick(State{}, s1, 1000, opt{}).State
	s2 := snap(W, panes(berthier), panes(pn("w1:p1"), pn("w3:p1")))
	crashed := func(c Cache) map[string]Obs {
		return map[string]Obs{"w1:p1": {Exited: yes(false), AtShell: yes(true), Cache: &c}}
	}
	cold := Cache{LastAt: 10000 - 3400, Context: 183000, Model: "claude-opus-5-5", TTL1h: true}

	check(t, "warm cache: Revived", len(tick(prev, s2, 10000, opt{obs: crashed(Cache{LastAt: 10000 - 3200, Context: 183000})}).Revive), `1`)
	check(t, "small context: Revived", len(tick(prev, s2, 10000, opt{obs: crashed(Cache{LastAt: 0, Context: 49000})}).Revive), `1`)
	o := tick(prev, s2, 10000, opt{obs: crashed(cold)})
	a := o.State.Agents.M["w1:p1"]
	check(t, "cold and big: Frozen, not Revived", []any{o.Revive, a.Status, a.Frozen, a.ReviveAttempts},
		`[[],"gone",{"since":10000,"why":"crash","last_at":6600,"context":183000,"model":"claude-opus-5-5","ttl_1h":true},0]`)
	check(t, "frozen logged", []any{o.Events[0].Kind, o.Events[0].Msg},
		`["frozen","froze a-w1-p1 in w1:p1: due a Revive with a cold cache (183k tokens, last call 56m ago); held for the user"]`)
	check(t, "Berthier hears", []any{escKeys(o.Escalate), o.Escalate[0].Text, o.Escalate[0].Ev["to"]},
		`[["frozen:w1:p1:10000"],"crashed with a cold cache (183k tokens, last call 56m ago): agent '-' in skipli (pane w1:p1), Revive ~$1.46","berthier"]`)
	check(t, "the user hears", []any{o.Speak[0].Key, o.Speak[0].Text}, `["speak_frozen:w1:p1:10000","skipli is frozen: Revive or Fresh Start?"]`)
	frozen := o.State

	inbox := []Ask{ask("1", "notes", "skipli", "hi", 9000), {ID: "b1", Kind: "board", From: "skipli", To: "skipli", Text: "[board] new task", At: 9000}}
	o = tick(frozen, s2, 20000, opt{inbox: inbox})
	check(t, "Frozen stays: no read, no Revive, no second Escalation, messages held, never stuck",
		[]any{looksAt(o, "w1:p1"), o.Revive, o.Escalate, o.Speak, o.Deliver, o.State.Agents.M["w1:p1"].Frozen.Since}, `[[],[],[],[],[],10000]`)

	o = tick(frozen, s2, 20000, opt{thaw: []Thaw{{ID: "t1", Pane: "w1:p1", How: "revive", At: 19990}}})
	a = o.State.Agents.M["w1:p1"]
	check(t, "the user's Revive", []any{o.Revive, o.Thawed, a.Frozen, a.ReviveAttempts},
		`[[{"pane_id":"w1:p1","name":"a-w1-p1","session_id":"sid-1","ev":{"pane":"w1:p1","corps":"skipli","name":"a-w1-p1","session":"sid-1","why":"user"}}],["t1"],null,1]`)
	o = tick(frozen, s2, 20000, opt{thaw: []Thaw{{ID: "t1", Pane: "w1:p1", How: "fresh", At: 19990}}})
	check(t, "the user's Fresh Start forgets the gone session", []any{o.Fresh, o.Thawed, o.State.Agents.Get("w1:p1") == nil, o.Revive},
		`[[{"ev":{"corps":"skipli","name":"a-w1-p1","pane":"w1:p1","session":"sid-1","why":"user"},"label":"skipli","model":"claude-opus-5-5","name":"a-w1-p1","pane_id":"w1:p1","session":"sid-1"}],["t1"],true,[]]`)
	o = tick(prev, s1, 20000, opt{thaw: []Thaw{{ID: "t2", Pane: "w1:p1", How: "revive"}}})
	check(t, "a request for an Agent not Frozen is dropped", []any{o.Revive, o.Fresh, o.Thawed}, `[[],[],["t2"]]`)
	// A Fresh Start's new session in the pane is not Frozen.
	s3 := snap(W, panes(ag("w1:p1", "idle", "sid-new", "", ""), berthier), panes(pn("w1:p1"), pn("w3:p1")))
	check(t, "new session is not Frozen", tick(frozen, s3, 20000, opt{}).State.Agents.M["w1:p1"].Frozen, `null`)

	// Usage limit: the resume is a Revive too.
	si := snap(W, panes(seq(ag("w1:p1", "idle", "sid-1", "skipli", ""), 2), berthier), panes(pn("w1:p1"), pn("w3:p1")))
	limited := tick(prev, si, 1100, opt{obs: map[string]Obs{"w1:p1": {LimitSeen: true, LimitReset: ptr(int64(5000))}}}).State
	o = Tick(TickIn{Prev: limited, Snap: si, Now: 5060, Cfg: testCfg, Divisions: D0})
	check(t, "resume reads the transcript first", []any{o.Resume, looksAt(o, "w1:p1")}, `[[],[{"pane_id":"w1:p1","session_id":"sid-1","want":["cache"]}]]`)
	o = tick(limited, si, 5060, opt{obs: map[string]Obs{"w1:p1": {Cache: &Cache{LastAt: 1090, Context: 120000, Model: "claude-sonnet-5-5"}}}})
	a = o.State.Agents.M["w1:p1"]
	check(t, "cold: Frozen, not resumed", []any{o.Resume, a.Frozen.Why, escKeys(o.Escalate), o.Escalate[0].Text},
		`[[],"limit",["frozen:w1:p1:5060"],"its usage limit reset with a cold cache (120k tokens, last call 66m ago): agent 'skipli' in skipli (pane w1:p1), Revive ~$0.30"]`)
	lf := o.State
	check(t, "Frozen holds asks to an idle Agent", tick(lf, si, 5070, opt{inbox: inbox[:1]}).Deliver, `[]`)
	check(t, "Frozen is never resumed by itself", []any{tick(lf, si, 9000, opt{}).Resume, looksAt(tick(lf, si, 9000, opt{}), "w1:p1")}, `[[],[]]`)
	o = tick(lf, si, 9000, opt{thaw: []Thaw{{ID: "t3", Pane: "w1:p1", How: "revive"}}})
	check(t, "the user's Revive resumes it", []any{o.Resume, o.Thawed, o.State.Agents.M["w1:p1"].Frozen, o.State.Agents.M["w1:p1"].Limit.Resumed},
		`[[{"pane_id":"w1:p1","name":"skipli","reset_at":5000,"ev":{"pane":"w1:p1","corps":"skipli","name":"skipli","session":"sid-1","why":"user"}}],["t3"],null,true]`)
	o = tick(lf, si, 9000, opt{thaw: []Thaw{{ID: "t3", Pane: "w1:p1", How: "revive"}}, obs: map[string]Obs{"w1:p1": {Draft: yes(true)}}})
	check(t, "a draft holds the user's Revive", []any{o.Resume, o.Thawed}, `[[],null]`)
	o = tick(lf, si, 9000, opt{thaw: []Thaw{{ID: "t4", Pane: "w1:p1", How: "fresh"}}})
	check(t, "Fresh Start of a live Claude", []any{o.Fresh[0].Live, o.Thawed, o.State.Agents.M["w1:p1"].Frozen}, `[true,["t4"],null]`)
	sw := snap(W, panes(seq(ag("w1:p1", "working", "sid-1", "skipli", ""), 3), berthier), panes(pn("w1:p1"), pn("w3:p1")))
	check(t, "the user prompting it thaws it", tick(lf, sw, 9000, opt{}).State.Agents.M["w1:p1"].Frozen, `null`)
	check(t, "a warm small one resumes", len(tick(limited, si, 5060, opt{obs: map[string]Obs{"w1:p1": {Cache: &Cache{LastAt: 1090, Context: 20000}}}}).Resume), `1`)
}

func looksAt(o TickOut, pane string) []Look {
	l := []Look{}
	for _, x := range o.Look {
		if x.PaneID == pane {
			l = append(l, x)
		}
	}
	return l
}

func TestFrozenRouting(t *testing.T) {
	dv := Divisions{"dark-mode": {Corps: "skipli", Worktree: true}}
	berthier := ag("w3:p1", "idle", "sid-b", "", "/home/u/berthier")
	all := snap(W, panes(ag("w1:p1", "idle", "sid-1", "", ""), ag("w2:p1", "idle", "sid-2", "", ""), berthier), panes(pn("w1:p1"), pn("w2:p1"), pn("w3:p1")))
	prev := tick(State{}, all, 1000, opt{divs: dv}).State
	cold := Cache{LastAt: 0, Context: 100000}
	f := cloneState(prev)
	f.Agents.M["w1:p1"].Frozen = &Frozen{Since: 1000, Why: "limit", Cache: cold}
	f.Escalated = map[string]int64{"frozen:w1:p1:1000": 1000, "speak_frozen:w1:p1:1000": 1000}
	blocked := snap(W, panes(ag("w1:p1", "idle", "sid-1", "", ""), ag("w2:p1", "blocked", "sid-2", "", ""), berthier), panes(pn("w1:p1"), pn("w2:p1"), pn("w3:p1")))
	f.Agents.M["w2:p1"].Status, f.Agents.M["w2:p1"].StatusSince = "blocked", 1000
	o := tick(f, blocked, 1200, opt{divs: dv})
	check(t, "a Frozen Main Agent commands nothing: its Division's trouble goes to Berthier", []any{escKeys(o.Escalate), o.Escalate[0].To}, `[["blocked:w2:p1:1000"],null]`)

	// Berthier Frozen: its escalations go to the user instead.
	fb := cloneState(prev)
	fb.Agents.M["w3:p1"].Frozen = &Frozen{Since: 1000, Why: "limit", Cache: cold}
	fb.Agents.M["w2:p1"].Status, fb.Agents.M["w2:p1"].StatusSince = "blocked", 1000
	o = tick(fb, blocked, 1200, opt{divs: dv})
	check(t, "Berthier Frozen: the user hears", []any{escKeys(o.Escalate), escKeys(o.Notify)}, `[["blocked:w2:p1:1000"],["frozen:w3:p1:1000"]]`)
	fb.Agents.M["w1:p1"].Frozen = &Frozen{Since: 1000, Why: "limit", Cache: cold}
	o = tick(fb, blocked, 1200, opt{divs: dv})
	keys := escKeys(o.Notify)
	slices.Sort(keys)
	check(t, "and of every Frozen Agent", []any{escKeys(o.Escalate), keys}, `[[],["blocked:w2:p1:1000","frozen:w1:p1:1000","frozen:w3:p1:1000"]]`)

	// Several frozen at once share one head, so the shell sends one prompt.
	two := cloneState(prev)
	two.Agents.M["w1:p1"].Frozen = &Frozen{Since: 1000, Why: "limit", Cache: cold}
	two.Agents.M["w2:p1"].Frozen = &Frozen{Since: 1000, Why: "limit", Cache: cold}
	o = tick(two, all, 1200, opt{divs: dv})
	check(t, "one Escalation listing them", []any{len(o.Escalate), o.Escalate[0].Head == o.Escalate[1].Head, o.Escalate[0].To, o.Escalate[1].To}, `[2,true,null,null]`)
	o = tick(two, all, 1200, opt{divs: dv, thaw: []Thaw{{ID: "f", Pane: "w2:p1", How: "fresh"}}})
	check(t, "a Division's Fresh Start names it for its Brief", o.Fresh[0].Division, `"dark-mode"`)
}
