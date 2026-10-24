package core

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"
)

// The pure decision core of the Main Loop. Tick takes the previous Grand Army
// State, Herdr's snapshot, the clock and everything else it needs, and returns
// the next State plus the actions to carry out. It performs none of them and
// reads nothing: the caller (internal/cli) observes, acts, logs and writes.
//
// A decision that needs a pane read waits: the pane is listed under Look and
// the caller runs Tick again with Obs. Tick alone writes the Grand Army State.
// Events are Tick's decisions for loop.log; each action carries Ev, the
// fields of the event the caller logs for its outcome.

// Config is the Main Loop's tunables (BERTHIER_* variables, seconds unless noted).
type Config struct {
	BerthierRoot   string
	BlockedAfter   int64
	LongAfter      int64
	DivisionSettle int64
	ReviveMax      int
	ReviveBackoff  int64
	ReviveReset    int64
	EscalationTTL  int64
	SpeakAfter     int64
	AskStuck       int64
	PingpongMax    int
	LimitGrace     int64
	LimitRetry     int64
	BudgetWarn     float64 // percent of an allowance
	QuotaGuardPct  float64 // percent of a 5h/7d window (0: 90)
	DraftQuiet     int64   // prompts to a pane wait this long after its input box last held the user's draft
	// Restart resume: a gap this long since the last state write may have cut
	// Agents off mid-turn; wait RestartWait for each to reappear (idle for
	// RestartSettle), then prompt it once.
	RestartGap, RestartWait, RestartSettle int64
	// A Revive or usage-limit resume goes ahead by itself only if the last
	// API call was less than CacheWarm ago or the context is under FrozenMin
	// tokens; otherwise the Agent is Frozen.
	CacheWarm, FrozenMin int64
}

// Snapshot is what the Host reports of the Grand Army's workspaces and panes.
// The Host adapter maps its own wire format to it; no JSON tags here.
type Snapshot struct {
	Workspaces []Workspace
	Agents     []SnapAgent
	Panes      []SnapAgent
}

type Workspace struct {
	ID       string
	Label    string
	Number   int
	Worktree *Worktree
}

// Worktree is where a worktree workspace's checkout lives.
type Worktree struct {
	RepoRoot     *string
	CheckoutPath *string
}

// SnapAgent is an agent, or a pane that may still hold a session, in a snapshot.
type SnapAgent struct {
	PaneID      string
	WorkspaceID string
	TabID       *string
	Agent       *string
	Status      string
	Cwd         *string
	Title       *string
	Name        *string
	StateSeq    *int64
	Session     *string
}

// Ask is a pending `berthier ask` (a file of state/inbox).
type Ask struct {
	ID   string `json:"id"`
	Kind string `json:"kind,omitempty"` // "notify": one-way, from a service; "board": one-way, a new card or task on a Board; "shutdown", "wake": one-way army-wide broadcasts, Text is the whole prompt
	From string `json:"from"`
	To   string `json:"to"`
	Text string `json:"text"`
	At   int64  `json:"at"`
}

// Obs is what the caller read from a pane Tick asked to look at.
type Obs struct {
	Exited       *bool
	AtShell      *bool
	NoTranscript *bool // its session has no saved transcript: nothing to resume
	LimitSeen    bool  // limit_reset was read (LimitReset nil: no limit stop)
	LimitReset   *int64
	Interrupted  *bool      // its transcript ends mid-turn
	Draft        *bool      // its input box holds text someone typed
	Cache        *Cache     // its transcript's prompt cache (zero: unknown)
	Auto         *AutoCheck // a Frozen Division's Brief and git state, for an automatic Fresh Start
}

// TickIn is everything Tick decides from.
type TickIn struct {
	Prev      State
	Snap      Snapshot
	Now       int64
	Cfg       Config
	Divisions Divisions
	Inbox     []Ask
	Usage     Usage
	Budgets   map[string]float64
	Obs       map[string]Obs
	Spend     SpendBrief
	Thaw      []Thaw // the user's choices for Frozen Agents
}

// SpendBrief is what the Main Loop needs of the Profile: this quota week's
// ranked Waste signals (for a Corps's budget warning) and last week's Spend
// (for the weekly optimizer review).
type SpendBrief struct {
	Waste         []WasteItem
	LastWeekUSD   float64
	LastWeekWaste float64
}

// Fields are an event's fields: whichever of pane, corps, name, session, to,
// why, ... it has.
type Fields map[string]any

// Event is a decision for loop.log.
type Event struct {
	Kind   string
	Msg    string
	Fields Fields
}

// Look asks the caller to read a pane.
type Look struct {
	PaneID    string   `json:"pane_id"`
	SessionID *string  `json:"session_id"`
	Want      []string `json:"want"`
}

type Revive struct {
	PaneID    string `json:"pane_id"`
	Name      string `json:"name"`
	SessionID string `json:"session_id"`
	Ev        Fields `json:"ev"`
}

type Rename struct {
	PaneID    string `json:"pane_id"`
	Name      string `json:"name"`
	SessionID string `json:"session_id"`
	Ev        Fields `json:"ev"`
}

type Resume struct {
	PaneID  string `json:"pane_id"`
	Name    string `json:"name"`
	ResetAt int64  `json:"reset_at"`
	Ev      Fields `json:"ev"`
}

// Escalation goes to a Main Agent (To is its pane; nil: Berthier), or, for
// Berthier's own trouble, to the user.
type Escalation struct {
	Key    string  `json:"key"`
	PaneID *string `json:"pane_id"`
	Name   *string `json:"name,omitempty"`
	To     *string `json:"to"`
	Head   string  `json:"head,omitempty"`
	Text   string  `json:"text"`
	Ev     Fields  `json:"ev"`
}

// restartTries is how many ticks a restart's continue nudge is attempted.
const restartTries = 5

// Proceed prompts an Agent a restart cut off mid-turn to continue.
type Proceed struct {
	PaneID string `json:"pane_id"`
	Name   string `json:"name"`
	Ev     Fields `json:"ev"`
}

type Speak struct {
	Key  string `json:"key"`
	Text string `json:"text"`
	Ev   Fields `json:"ev"`
}

type Delivery struct {
	ID     string `json:"id"`
	PaneID string `json:"pane_id"`
	From   string `json:"from"`
	To     string `json:"to"`
	Text   string `json:"text"`
	Oneway bool   `json:"oneway"`
	Wake   bool   `json:"wake,omitempty"` // clear the Agent's context first, then send Text plus what it needs to rebuild
	Ev     Fields `json:"ev"`
}

// TickOut is the next state and the actions to carry out.
type TickOut struct {
	State    State        `json:"state"`
	Look     []Look       `json:"look"`
	Events   []Event      `json:"events"`
	Revive   []Revive     `json:"revive"`
	Rename   []Rename     `json:"rename"`
	Resume   []Resume     `json:"resume"`
	Proceed  []Proceed    `json:"proceed"`
	Escalate []Escalation `json:"escalate"`
	Notify   []Escalation `json:"notify"`
	Speak    []Speak      `json:"speak"`
	Deliver  []Delivery   `json:"deliver"`
	Fresh    []FreshStart `json:"fresh"`
	Thawed   []string     `json:"thawed"` // Thaw requests decided: drop them
}

func ptr[T any](v T) *T { return &v }

func strOr(p *string, d string) string {
	if p == nil {
		return d
	}
	return *p
}

func eqStr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func isBerthierCwd(cwd *string, root string) bool {
	c := strOr(cwd, "")
	return c == root || strings.HasPrefix(c, root+"/")
}

func minutes(secs int64) int64 { return int64(math.Floor(float64(secs) / 60)) }

func round1(f float64) string { return Num(math.Round(f*10) / 10) }

// Tick decides one pass of the Main Loop.
func Tick(in TickIn) TickOut {
	cfg, now, div := in.Cfg, in.Now, in.Divisions
	obs := func(p string) Obs { return in.Obs[p] }
	corpsOfLabel := func(a *Agent) string { return div.Owner(strOr(a.WorkspaceLabel, "")) }

	// Herdr's own workspace order, which every view of the Grand Army follows.
	workspaces := append([]Workspace(nil), in.Snap.Workspaces...)
	slices.SortStableFunc(workspaces, func(a, b Workspace) int { return cmp.Compare(a.Number, b.Number) })
	ws := map[string]Workspace{}
	for _, w := range workspaces {
		ws[w.ID] = w
	}
	paneIDs := map[string]bool{}
	for _, p := range in.Snap.Panes {
		paneIDs[p.PaneID] = true
	}

	// --- Weekly budgets: a Corps at its allowance has its Revives, usage-limit
	// resumes and ask deliveries held (soft: nothing is stopped), and its Main
	// Agent and the user hear at the warning level, once per week.
	var budget []CorpsUse
	for _, c := range in.Usage.View(in.Budgets, now).Corps {
		if c.Allowance != nil && *c.Allowance > 0 {
			budget = append(budget, c)
		}
	}
	week := Num(orF(in.Usage.ResetsAt, 0))
	over := map[string]bool{}
	for _, c := range budget {
		if *c.Ratio >= 1 {
			over[c.Name] = true
		}
	}
	prevAgents := &in.Prev.Agents
	prevEsc := in.Prev.Escalated

	// --- Live agents: carry over memory (session id, timers, revive bookkeeping).
	// A pane that still holds a Claude session with no agent listed is Herdr
	// resuming it after a restart (a crash clears the pane's session): it is live.
	agentPanes := map[string]bool{}
	for _, a := range in.Snap.Agents {
		agentPanes[a.PaneID] = true
	}
	listed := append([]SnapAgent(nil), in.Snap.Agents...)
	for _, p := range in.Snap.Panes {
		if p.Session != nil && !agentPanes[p.PaneID] {
			listed = append(listed, p)
		}
	}
	live := Agents{}
	for _, a := range listed {
		p := prevAgents.Get(a.PaneID)
		var pv Agent
		if p != nil {
			pv = *p
		}
		same := p != nil && pv.Status == a.Status
		sid := a.Session
		if sid == nil {
			sid = pv.SessionID
		}
		var name *string
		if a.Name != nil {
			name = a.Name
		} else if eqStr(sid, pv.SessionID) {
			name = pv.Name
		}
		kind := a.Agent
		if kind == nil {
			kind = pv.Kind
		}
		v := &Agent{
			Name:           name,
			Kind:           kind,
			Status:         a.Status,
			StatusSince:    now,
			PrevStatus:     nilIfEmpty(pv.Status),
			SessionID:      sid,
			WorkspaceID:    a.WorkspaceID,
			TabID:          a.TabID,
			Cwd:            a.Cwd,
			Title:          a.Title,
			StateSeq:       a.StateSeq,
			Limit:          pv.Limit,
			LimitResumedAt: pv.LimitResumedAt,
			LastReviveAt:   pv.LastReviveAt,
			DraftAt:        pv.DraftAt,
		}
		if w, ok := ws[a.WorkspaceID]; ok {
			v.WorkspaceLabel = ptr(w.Label)
		}
		if same {
			v.StatusSince, v.PrevStatus = pv.StatusSince, pv.PrevStatus
		}
		// Frozen until the user chooses, or until the Agent works again (the user prompted it).
		if pv.Frozen != nil && eqStr(sid, pv.SessionID) && (a.Status == "idle" || a.Status == "done") {
			v.Frozen = pv.Frozen
		}
		if orI(pv.LastReviveAt, 0) > now-cfg.ReviveReset {
			v.ReviveAttempts = pv.ReviveAttempts
		}
		live.Set(a.PaneID, v)
	}

	// --- Agents that vanished. A pane still present means the process died and
	// left its shell behind: keep it as gone and try to Revive. A closed pane is
	// a deliberate stop: forget the agent.
	gone := Agents{}
	for _, k := range prevAgents.Keys {
		a := prevAgents.M[k]
		if live.Get(k) != nil || a.SessionID == nil || strOr(a.Kind, "") != "claude" || !paneIDs[k] {
			continue
		}
		c := *a
		if c.Status != "gone" {
			c.PrevStatus, c.Status, c.StatusSince = nilIfEmpty(c.Status), "gone", now
		}
		if w, ok := ws[c.WorkspaceID]; ok {
			c.WorkspaceLabel = ptr(w.Label)
		}
		gone.Set(k, &c)
	}

	// --- Agents that vanished this very tick, and gone ones due a Revive.
	var vanished, due []string
	for _, k := range gone.Keys {
		g := gone.M[k]
		if prevAgents.M[k].Status != "gone" {
			vanished = append(vanished, k)
		}
		if g.Frozen == nil && g.ReviveAttempts < cfg.ReviveMax && !over[corpsOfLabel(g)] && now-orI(g.LastReviveAt, 0) >= cfg.ReviveBackoff {
			due = append(due, k)
		}
	}
	// A clean exit is the user's choice: forget the agent instead of Reviving.
	// Read when it vanishes, while the evidence is still at the bottom of its
	// pane, and again right before any Revive.
	exitCheck := uniq(append(append([]string(nil), vanished...), due...))
	var exited, unsaved, reviving []string
	for _, k := range exitCheck {
		if o := obs(k); o.Exited != nil && *o.Exited {
			exited = append(exited, k)
		} else if o.NoTranscript != nil && *o.NoTranscript {
			exited = append(exited, k)
			unsaved = append(unsaved, k)
		}
	}
	// A Revive resumes the session: by itself only while its cache is warm or
	// its context small, else the Agent is Frozen for the user to decide.
	type freeze struct {
		pane, why string
		cache     Cache
	}
	var freezing []freeze
	for _, k := range due {
		if o := obs(k); o.Exited != nil && !*o.Exited && o.AtShell != nil && o.Cache != nil && !slices.Contains(exited, k) {
			if o.Cache.Warm(now, cfg) {
				reviving = append(reviving, k)
			} else {
				freezing = append(freezing, freeze{k, "crash", *o.Cache})
			}
		}
	}
	remaining := Agents{}
	for _, k := range gone.Keys {
		if !slices.Contains(exited, k) {
			remaining.Set(k, gone.M[k])
		}
	}
	gone = remaining

	agents := Agents{}
	for _, k := range live.Keys {
		agents.Set(k, live.M[k])
	}
	for _, k := range gone.Keys {
		agents.Set(k, gone.M[k])
	}
	// Berthier: the first live agent in Berthier's own repository, else a gone one.
	var berthier *string
	for pass := 0; pass < 2 && berthier == nil; pass++ {
		for _, k := range agents.Keys {
			a := agents.M[k]
			if isBerthierCwd(a.Cwd, cfg.BerthierRoot) && (pass == 1 || a.Status != "gone") {
				berthier = ptr(k)
				break
			}
		}
	}
	berthierWS := ""
	if berthier != nil {
		berthierWS = agents.M[*berthier].WorkspaceID
	}
	// Who an event is about, by pane.
	about := func(p string) Fields {
		a := agents.Get(p)
		if a == nil {
			a = prevAgents.Get(p)
		}
		corps, name := "", FallbackName(p)
		if a != nil {
			corps = CorpsName(strOr(a.WorkspaceLabel, ""))
			if a.Name != nil {
				name = *a.Name
			}
		}
		if berthier != nil && p == *berthier {
			corps = "berthier"
		}
		return Fields{"pane": p, "corps": corps, "name": name}
	}
	with := func(f Fields, kv ...any) Fields {
		g := Fields{}
		for k, v := range f {
			g[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			g[kv[i].(string)] = kv[i+1]
		}
		return g
	}
	nameOf := func(k string, a *Agent) string {
		if a.Name != nil {
			return *a.Name
		}
		return FallbackName(k)
	}

	// --- Drafts: never type into an input box holding the user's text (they
	// may be dictating). Every prompt to a pane waits for a read of its box,
	// and is held while it holds a draft and for draft_quiet after the box was
	// last seen holding one, so a pause in dictation is not taken for done.
	draftLook := map[string]bool{}
	held := func(p string) bool {
		o := obs(p)
		if o.Draft == nil {
			draftLook[p] = true
			return true
		}
		a := live.Get(p)
		return *o.Draft || (a != nil && a.DraftAt != nil && now-*a.DraftAt < cfg.DraftQuiet)
	}

	// --- Revive: resume the gone session in the pane it died in, if the pane is
	// back at its shell. Every try counts towards revive_max.
	revive := []Revive{}
	for _, k := range reviving {
		if o := obs(k); *o.AtShell {
			a := agents.M[k]
			revive = append(revive, Revive{k, nameOf(k, a), strOr(a.SessionID, ""), with(about(k), "session", a.SessionID)})
		}
	}

	// --- Frozen Agents the user chose for: a crashed one is Revived, one
	// stopped on its usage limit resumed (once its box holds no draft), or
	// either gets a Fresh Start. A request for an Agent that is not Frozen is
	// dropped.
	var thawed, unfreeze []string
	fresh := []FreshStart{}
	var thawResume []Resume
	thaws := append([]Thaw(nil), in.Thaw...)
	slices.SortStableFunc(thaws, func(a, b Thaw) int { return cmp.Compare(a.At, b.At) })
	for _, t := range thaws {
		a := agents.Get(t.Pane)
		if a == nil || a.Frozen == nil || slices.Contains(unfreeze, t.Pane) {
			thawed = append(thawed, t.ID)
			continue
		}
		ev := with(about(t.Pane), "session", a.SessionID, "why", "user")
		switch {
		case t.How == "fresh":
			division := ""
			if label := strOr(a.WorkspaceLabel, ""); div.IsDivision(label) {
				division = CorpsName(label)
			}
			fresh = append(fresh, FreshStart{PaneID: t.Pane, Name: nameOf(t.Pane, a), Session: strOr(a.SessionID, ""), Model: a.Frozen.Model,
				Division: division, Label: strOr(a.WorkspaceLabel, ""), Live: a.Status != "gone", Ev: ev})
			if a.Status == "gone" {
				// Its session is over: the next tick finds the new one in the pane (or nothing, if it failed to start).
				agents.Delete(t.Pane)
				thawed = append(thawed, t.ID)
				continue
			}
		case a.Status == "gone":
			revive = append(revive, Revive{t.Pane, nameOf(t.Pane, a), strOr(a.SessionID, ""), ev})
			reviving = append(reviving, t.Pane)
		case held(t.Pane):
			continue // the request waits for the user's draft
		default:
			thawResume = append(thawResume, Resume{t.Pane, nameOf(t.Pane, a), orI(a.Limit.resetAt(), 0), ev})
		}
		thawed = append(thawed, t.ID)
		unfreeze = append(unfreeze, t.Pane)
	}

	// --- Names Herdr lost: give each listed agent back its session's name,
	// unless another agent took it meanwhile.
	taken := map[string]bool{}
	for _, a := range in.Snap.Agents {
		if a.Name != nil {
			taken[*a.Name] = true
		}
	}
	rename := []Rename{}
	for _, a := range in.Snap.Agents {
		v := live.M[a.PaneID]
		if a.Name != nil || v.Name == nil || !eqStr(a.Session, v.SessionID) || taken[*v.Name] {
			continue
		}
		rename = append(rename, Rename{a.PaneID, *v.Name, strOr(v.SessionID, ""), with(about(a.PaneID), "session", v.SessionID)})
	}

	// --- Usage limits: Claude often never continues by itself once its limit
	// resets. Read each idle episode's pane once for a limit stop; once the limit
	// has reset, nudge it once per episode, and never twice within limit_retry.
	// An episode is read on its first tick, so it stopped about now.
	type idleEp struct {
		pane    string
		a       *Agent
		episode string
	}
	var idle, limitCheck, limitRead []idleEp
	for _, k := range live.Keys {
		a := live.M[k]
		if a.Status == "idle" || a.Status == "done" {
			seq := "null"
			if a.StateSeq != nil {
				seq = fmt.Sprint(*a.StateSeq)
			}
			idle = append(idle, idleEp{k, a, fmt.Sprintf("%s:%d", seq, a.StatusSince)})
		}
	}
	for _, e := range idle {
		if e.a.Limit == nil || e.a.Limit.Episode != e.episode {
			limitCheck = append(limitCheck, e)
		}
	}
	for _, e := range limitCheck {
		if o := obs(e.pane); o.LimitSeen {
			c := *e.a
			c.Limit = &Limit{Episode: e.episode, Since: now, ResetAt: o.LimitReset}
			limitRead = append(limitRead, idleEp{e.pane, &c, e.episode})
		}
	}
	resume := []Resume{}
	var cacheLook []string
	for _, e := range idle {
		v := e.a
		for _, r := range limitRead {
			if r.pane == e.pane {
				v = r.a
				break
			}
		}
		if v.Frozen == nil && v.Limit != nil && v.Limit.Episode == e.episode && v.Limit.ResetAt != nil && !v.Limit.Resumed &&
			now >= *v.Limit.ResetAt+cfg.LimitGrace && now >= orI(v.LimitResumedAt, 0)+cfg.LimitRetry &&
			!over[corpsOfLabel(v)] {
			// A resume, like a Revive, rewrites a cold cache: Frozen instead.
			if c := obs(e.pane).Cache; c == nil {
				cacheLook = append(cacheLook, e.pane)
			} else if !c.Warm(now, cfg) {
				freezing = append(freezing, freeze{e.pane, "limit", *c})
			} else if !held(e.pane) {
				resume = append(resume, Resume{e.pane, nameOf(e.pane, v), *v.Limit.ResetAt, about(e.pane)})
			}
		}
	}

	resume = append(resume, thawResume...)
	// Frozen this tick (newly, or still): no prompt reaches them, nor does it
	// command anything, until the user has chosen and it is back.
	frozen := map[string]*Frozen{}
	for _, k := range agents.Keys {
		if f := agents.M[k].Frozen; f != nil {
			frozen[k] = f
		}
	}
	for _, f := range freezing {
		frozen[f.pane] = &Frozen{Since: now, Why: f.why, Cache: f.cache}
	}

	// --- Restart: Herdr resumes every session but they come back idle, even
	// those cut off mid-turn. A gap since the last state write (loop down,
	// Herdr unreachable) means the Agents working before it may have been
	// interrupted: remember them by session in the state until each reappears,
	// since Herdr may list them over several ticks. A reappeared idle one is
	// read for an unfinished turn in its transcript (Obs.Interrupted) and
	// prompted once if so; one that is working, blocked or finished is
	// forgotten, one never relisted expires.
	waiting := map[string]Interrupt{}
	for sid, w := range in.Prev.Interrupted {
		if w.Since > now-orDefault(cfg.RestartWait, 3600) {
			waiting[sid] = w
		}
	}
	if in.Prev.UpdatedAt != 0 && now-in.Prev.UpdatedAt > orDefault(cfg.RestartGap, 45) {
		for _, k := range prevAgents.Keys {
			a := prevAgents.M[k]
			if a.Status == "working" && strOr(a.Kind, "") == "claude" && a.SessionID != nil {
				waiting[*a.SessionID] = Interrupt{Name: a.Name, Since: now}
			}
		}
	}
	type present struct {
		pane, label, status string
		since               int64
	}
	bySession := map[string]present{}
	for _, k := range live.Keys {
		a := live.M[k]
		if a.SessionID == nil {
			continue
		}
		st := "unknown" // a pane Herdr is still resuming has no agent to prompt yet
		if agentPanes[k] {
			st = a.Status
		}
		bySession[*a.SessionID] = present{k, strOr(a.WorkspaceLabel, ""), st, a.StatusSince}
	}
	type back struct {
		sid  string
		name *string
		present
	}
	var backs, cutAgents, goAgents []back
	for _, sid := range slices.Sorted(maps.Keys(waiting)) {
		b, ok := bySession[sid]
		// Herdr first lists a resumed pane with its stale status, then
		// relaunches Claude: wait for the status to hold.
		if ok && (b.status == "idle" || b.status == "done") && now-b.since >= orDefault(cfg.RestartSettle, 20) {
			backs = append(backs, back{sid, waiting[sid].Name, b})
		}
	}
	for _, b := range backs {
		if o := obs(b.pane); o.Interrupted != nil && *o.Interrupted {
			cutAgents = append(cutAgents, b)
			if !over[div.Owner(b.label)] && !held(b.pane) { // a Corps at its budget, or a draft, keeps its Agents waiting
				goAgents = append(goAgents, b)
			}
		}
	}
	inBacks := func(l []back, pane string) bool {
		for _, b := range l {
			if b.pane == pane {
				return true
			}
		}
		return false
	}
	// A nudge may not take (Herdr can still list the pane without its agent):
	// the session stays remembered with its tries so far. A taken one shows as
	// a working Agent or a finished turn on a later tick and is forgotten then;
	// after restartTries nudges a still-untaken one is given up on.
	proceed := []Proceed{}
	retry := map[string]int{}
	var gaveUp []Event
	for _, b := range goAgents {
		name := FallbackName(b.pane)
		if v := live.M[b.pane].Name; v != nil {
			name = *v
		} else if b.name != nil {
			name = *b.name
		}
		ev := with(about(b.pane), "session", b.sid)
		if tries := waiting[b.sid].Tries; tries >= restartTries {
			gaveUp = append(gaveUp, Event{"continue_failed", fmt.Sprintf("continue failed: %s in %s (restart): %d nudges did not take", name, b.pane, tries), ev})
			continue
		} else {
			retry[b.sid] = tries + 1
		}
		proceed = append(proceed, Proceed{b.pane, name, ev})
	}
	kept := map[string]Interrupt{}
	for sid, w := range waiting {
		b, ok := bySession[sid]
		if n, again := retry[sid]; again {
			kept[sid] = Interrupt{w.Name, w.Since, n}
		} else if !ok || b.status == "unknown" ||
			((b.status == "idle" || b.status == "done") &&
				(obs(b.pane).Interrupted == nil || (inBacks(cutAgents, b.pane) && !inBacks(goAgents, b.pane)))) {
			kept[sid] = w
		}
	}

	// --- Panes to read before the decisions above can be made.
	look := []Look{}
	for _, k := range exitCheck {
		if o := obs(k); o.Exited == nil {
			want := []string{"exited", "no_transcript"}
			if slices.Contains(due, k) {
				want = append(want, "at_shell", "cache")
			}
			look = append(look, Look{k, prevAgents.M[k].SessionID, want})
		} else if slices.Contains(due, k) && o.Cache == nil {
			look = append(look, Look{k, prevAgents.M[k].SessionID, []string{"cache"}})
		}
	}
	for _, k := range cacheLook {
		look = append(look, Look{k, live.M[k].SessionID, []string{"cache"}})
	}
	for _, e := range limitCheck {
		if !obs(e.pane).LimitSeen {
			look = append(look, Look{e.pane, e.a.SessionID, []string{"limit_reset"}})
		}
	}

	for _, b := range backs {
		if obs(b.pane).Interrupted == nil {
			look = append(look, Look{b.pane, ptr(b.sid), []string{"interrupted"}})
		}
	}

	// A Corps newly at its allowance this week: logged and spoken once.
	type block struct{ key, corps, text string }
	var blocks []block
	for _, c := range budget {
		key := fmt.Sprintf("budget_block:%s:%s", c.Name, week)
		if *c.Ratio >= 1 && prevEsc[key] == 0 {
			blocks = append(blocks, block{key, c.Name,
				fmt.Sprintf("%s used %s%% of the week, over its %s%% budget", c.Name, round1(c.Used), Num(*c.Allowance))})
		}
	}

	decided := append([]Event{}, gaveUp...)
	sessionOf := func(p string) *string {
		if a := live.M[p]; a != nil {
			return a.SessionID
		}
		return gone.M[p].SessionID
	}

	// --- A Frozen Division is given a Fresh Start by itself, once per episode,
	// when nothing can be lost; else it stays Frozen, and the escalation says why.
	autoPending := map[string]bool{}
	for _, k := range agents.Keys {
		f, a := frozen[k], agents.M[k]
		if f == nil || f.Auto != "" || slices.Contains(unfreeze, k) || k == strOr(berthier, "") || !div.IsDivision(strOr(a.WorkspaceLabel, "")) {
			continue
		}
		o := obs(k).Auto
		if o == nil {
			autoPending[k] = true
			look = append(look, Look{k, a.SessionID, []string{"auto_fresh"}})
			continue
		}
		ev := with(about(k), "session", a.SessionID, "why", "frozen")
		if ok, why := o.Verdict(); !ok {
			c := *f
			c.Auto = "refused:" + why
			frozen[k] = &c
			a.Frozen = &c
			decided = append(decided, Event{"auto_fresh_refused", fmt.Sprintf("auto Fresh Start refused for %s in %s: %s; stays Frozen", ev["name"], k, why), with(ev, "reason", why)})
			continue
		}
		decided = append(decided, Event{"auto_fresh", fmt.Sprintf("auto Fresh Start for %s in %s: Brief kept, worktree clean", ev["name"], k), ev})
		label := strOr(a.WorkspaceLabel, "")
		fresh = append(fresh, FreshStart{PaneID: k, Name: nameOf(k, a), Session: strOr(a.SessionID, ""), Model: f.Model,
			Division: CorpsName(label), Label: label, Live: a.Status != "gone", Ev: ev})
		if a.Status == "gone" {
			agents.Delete(k)
		} else {
			unfreeze = append(unfreeze, k)
		}
	}
	for _, k := range exited {
		f := with(about(k), "session", prevAgents.M[k].SessionID)
		why := "user exited it"
		if slices.Contains(unsaved, k) {
			why = "its session has no transcript to resume"
		}
		decided = append(decided, Event{"forgot", fmt.Sprintf("forgot %s in %s: %s (session %s)", f["name"], k, why, strOr(prevAgents.M[k].SessionID, "null")), f})
	}
	for _, k := range reviving {
		if o := obs(k); o.AtShell != nil && !*o.AtShell {
			decided = append(decided, Event{"revive_skipped", fmt.Sprintf("revive skipped: pane %s is not at a shell prompt", k), about(k)})
		}
	}
	for _, e := range limitRead {
		if e.a.Limit.ResetAt == nil {
			continue
		}
		f := with(about(e.pane), "until", *e.a.Limit.ResetAt)
		decided = append(decided, Event{"limit_stop", fmt.Sprintf("usage limit: %s in %s stopped until %s", f["name"], e.pane,
			time.Unix(*e.a.Limit.ResetAt, 0).Local().Format("2006-01-02 15:04 MST")), f})
	}
	for _, b := range blocks {
		decided = append(decided, Event{"budget_exhausted",
			b.text + ": holding Revives, usage-limit resumes and ask delivery for it and its Divisions until the week resets",
			Fields{"corps": b.corps}})
	}

	// --- Each Corps's Main Agent: a live Agent in the Corps's own workspace.
	mainOf := map[string]string{} // by workspace label
	{
		byLabel := map[string][]string{}
		var labels []string
		for _, k := range agents.Keys {
			a := agents.M[k]
			if a.Status == "gone" || frozen[k] != nil || div.IsDivision(strOr(a.WorkspaceLabel, "")) {
				continue
			}
			l := strOr(a.WorkspaceLabel, "")
			if _, ok := byLabel[l]; !ok {
				labels = append(labels, l)
			}
			byLabel[l] = append(byLabel[l], k)
		}
		for _, l := range labels {
			mainOf[l] = byLabel[l][0]
		}
	}
	mainNamed := map[string]string{}
	{
		for _, l := range slices.Sorted(maps.Keys(mainOf)) {
			mainNamed[CorpsName(l)] = mainOf[l]
		}
	}

	// --- Warning level reached: the Corps's Main Agent hears (Berthier when it has none).
	warnAt := cfg.BudgetWarn
	if warnAt == 0 {
		warnAt = 80
	}
	var warn []Escalation
	for _, c := range budget {
		if !(*c.Ratio >= warnAt/100 && *c.Ratio < 1) {
			continue
		}
		var to *string
		if p, ok := mainNamed[c.Name]; ok {
			to = ptr(p)
		}
		pct := int64(math.Floor(*c.Ratio * 100))
		waste := ""
		if l := WasteLine(in.Spend.Waste, c.Name, 3); l != "" {
			waste = fmt.Sprintf(". Its top Waste signals this week: %s; details: berthier profile --waste --corps %s", l, c.Name)
		}
		var paneID *string
		if to != nil && berthier != nil && *to == *berthier {
			paneID = berthier
		}
		warn = append(warn, Escalation{
			Key: fmt.Sprintf("budget_warn:%s:%s", c.Name, week), PaneID: paneID, To: to,
			Head: "[berthier-loop] Weekly budget:",
			Text: fmt.Sprintf("%s has used %s%% of the week, %d%% of its %s%% budget: wind down low-value work, since at 100%% its Revives, usage-limit resumes and ask delivery are held%s",
				c.Name, round1(c.Used), pct, Num(*c.Allowance), waste),
			Ev: Fields{"why": "budget_warn", "reason": fmt.Sprintf("%d%% of weekly budget", pct), "corps": c.Name, "pct": pct},
		})
	}

	// --- Asks: deliver each to its addressee's Agent once it is ready for input,
	// at most one per Agent per tick, oldest first.
	type addressable struct{ pane, status, corps, name string }
	var addr []addressable
	heldFor := map[string]bool{} // Corps names whose Agent is Frozen: their messages wait
	for _, k := range agents.Keys {
		a := agents.M[k]
		if frozen[k] != nil {
			heldFor[CorpsName(strOr(a.WorkspaceLabel, ""))] = true
			if berthier != nil && k == *berthier {
				heldFor["berthier"] = true
			}
			continue
		}
		if a.Status == "gone" {
			continue
		}
		if berthier != nil && k == *berthier {
			addr = append(addr, addressable{k, a.Status, "berthier", "berthier"})
			// Berthier is also the Main Agent of the Corps of its own repo (its Board messages are addressed to that).
			if l := CorpsName(strOr(a.WorkspaceLabel, "")); l != "" && l != "berthier" {
				addr = append(addr, addressable{k, a.Status, "berthier", l})
			}
		} else {
			addr = append(addr, addressable{k, a.Status, corpsOfLabel(a), CorpsName(strOr(a.WorkspaceLabel, ""))})
		}
	}
	inbox := append([]Ask(nil), in.Inbox...)
	slices.SortStableFunc(inbox, func(a, b Ask) int { return cmp.Compare(a.At, b.At) })
	deliver := []Delivery{}
	used := map[string]bool{}
	delivered := map[string]bool{}
	for _, m := range inbox {
		to := CorpsName(m.To)
		for _, o := range addr {
			if o.name == to && (o.status == "idle" || o.status == "done") && (!over[o.corps] || IsBroadcast(m.Kind)) && !used[o.pane] && !held(o.pane) {
				used[o.pane] = true
				delivered[m.ID] = true
				text := fmt.Sprintf("[berthier ask] from %s: %s (Reply or report back with: berthier ask %s \"...\")", m.From, m.Text, m.From)
				if m.Kind == "notify" {
					text = fmt.Sprintf("[berthier notify] from %s: %s", m.From, m.Text)
				}
				if m.Kind == "board" || IsBroadcast(m.Kind) {
					text = m.Text
				}
				deliver = append(deliver, Delivery{m.ID, o.pane, m.From, to, text, m.Kind == "notify" || m.Kind == "board" || IsBroadcast(m.Kind), m.Kind == "wake",
					Fields{"pane": o.pane, "from": m.From, "to": to, "ask": m.ID}})
				break
			}
		}
	}
	quota := quotaGuard(in.Usage.View(in.Budgets, now), cfg.QuotaGuardPct, quotaAgents(agents, frozen, used), held, prevEsc)
	deliver = append(deliver, quota.Deliver...)
	askLog := []AskLogEntry{}
	for _, e := range in.Prev.AskLog {
		if e.At > now-3600 {
			askLog = append(askLog, e)
		}
	}
	for _, d := range deliver {
		if !d.Oneway {
			askLog = append(askLog, AskLogEntry{d.From, d.To, now})
		}
	}

	// --- Escalation candidates. Keys embed status_since so a new episode of the
	// same condition escalates again, but one episode escalates only once.
	var cands []Escalation
	for _, pane := range agents.Keys {
		v := agents.M[pane]
		age := now - v.StatusSince
		where := strOr(v.WorkspaceLabel, v.WorkspaceID)
		who := fmt.Sprintf("agent '%s' in %s (pane %s)", strOr(v.Name, "-"), where, pane)
		label := strOr(v.WorkspaceLabel, "")
		// A Division's trouble goes to its Corps's Main Agent, which commands it.
		var to *string
		if div.IsDivision(label) {
			if p, ok := mainOf[div[CorpsName(label)].Corps]; ok {
				to = ptr(p)
			}
		}
		var why, reason, also string
		switch {
		case v.Status == "gone" && v.ReviveAttempts >= cfg.ReviveMax:
			why, reason, also = "revive_failed", fmt.Sprintf("revive failed %dx", v.ReviveAttempts), ", session "+strOr(v.SessionID, "null")
		case v.Status == "blocked" && age >= cfg.BlockedAfter:
			why, reason = "blocked", fmt.Sprintf("blocked %dm on an approval or question", minutes(age))
		case v.Status == "working" && age >= cfg.LongAfter:
			why, reason = "long", fmt.Sprintf("working %dm without stopping", minutes(age))
		// A spawned Division that stops may be done: its Corps reviews it.
		case div[CorpsName(label)].Worktree && (v.Status == "idle" || v.Status == "done") &&
			strOr(v.PrevStatus, "") == "working" && age >= cfg.DivisionSettle:
			why, reason = "division_stopped", "division agent stopped working (finished or stalled)"
		default:
			continue
		}
		cands = append(cands, Escalation{
			Key: fmt.Sprintf("%s:%s:%d", why, pane, v.StatusSince), PaneID: ptr(pane), Name: v.Name, To: to,
			Text: fmt.Sprintf("%s: %s%s", reason, who, also), Ev: with(about(pane), "why", why, "reason", reason),
		})
	}
	for _, m := range in.Inbox {
		if m.Kind == "notify" || m.Kind == "board" || IsBroadcast(m.Kind) || now-m.At < cfg.AskStuck || delivered[m.ID] || heldFor[CorpsName(m.To)] {
			continue
		}
		reason := fmt.Sprintf("undelivered %dm", minutes(now-m.At))
		cands = append(cands, Escalation{
			Key:  "ask_stuck:" + m.ID,
			Text: fmt.Sprintf("ask from %s to %s %s: %s", m.From, m.To, reason, m.Text),
			Ev:   Fields{"why": "ask_stuck", "reason": reason, "ask": m.ID, "ask_from": m.From, "ask_to": CorpsName(m.To)},
		})
	}
	// Ping-pong: replies flipping direction again and again; one-way updates
	// don't count, nor a Corps commanding and reviewing its own Division.
	{
		type entry struct {
			AskLogEntry
			pair [2]string
		}
		groups := map[[2]string][]entry{}
		for _, e := range askLog {
			if CorpsName(div[e.From].Corps) == e.To || CorpsName(div[e.To].Corps) == e.From {
				continue
			}
			pair := [2]string{e.From, e.To}
			if pair[0] > pair[1] {
				pair[0], pair[1] = pair[1], pair[0]
			}
			groups[pair] = append(groups[pair], entry{e, pair})
		}
		pairs := make([][2]string, 0, len(groups))
		for p := range groups {
			pairs = append(pairs, p)
		}
		slices.SortFunc(pairs, func(a, b [2]string) int { return slices.Compare(a[:], b[:]) })
		for _, p := range pairs {
			l := groups[p]
			slices.SortStableFunc(l, func(a, b entry) int { return cmp.Compare(a.At, b.At) })
			flips := 0
			for i := 1; i < len(l); i++ {
				if l[i].From != l[i-1].From {
					flips++
				}
			}
			if flips < cfg.PingpongMax {
				continue
			}
			reason := fmt.Sprintf("%d back-and-forths in the last hour", flips)
			pair := p[0] + " and " + p[1]
			cands = append(cands, Escalation{
				Key:  fmt.Sprintf("pingpong:%s:%d", pair, int64(math.Floor(float64(now)/3600))),
				Text: fmt.Sprintf("asks bouncing between %s: %s", pair, reason),
				Ev:   Fields{"why": "pingpong", "reason": reason, "ask_from": p[0], "ask_to": p[1]},
			})
		}
	}
	cands = append(cands, warn...)

	// --- Frozen: Berthier hears once per episode, and tells the user, who
	// alone chooses. Several frozen at once (a usage limit reset) go as one prompt.
	for _, pane := range agents.Keys {
		f := frozen[pane]
		if f == nil || slices.Contains(unfreeze, pane) || autoPending[pane] {
			continue
		}
		v := agents.M[pane]
		what := "crashed"
		if f.Why == "limit" {
			what = "its usage limit reset"
		}
		cost := ""
		if usd, ok := f.ReviveUSD(); ok {
			cost = fmt.Sprintf(" ~$%.2f", usd)
		}
		reason := fmt.Sprintf("%s with a cold cache (%s tokens, last call %dm ago)", what, tokensK(f.Context), minutes(now-f.LastAt))
		if why, ok := strings.CutPrefix(f.Auto, "refused:"); ok {
			reason += "; auto Fresh Start refused: " + why
		}
		cands = append(cands, Escalation{
			Key: fmt.Sprintf("frozen:%s:%d", pane, f.Since), PaneID: ptr(pane), Name: v.Name,
			Head: "[berthier-loop] Frozen Agents: resuming rewrites their whole context, so their messages are held until the user chooses Revive or Fresh Start (Console, or berthier revive|fresh <pane>); tell the user, never choose for them:",
			Text: fmt.Sprintf("%s: agent '%s' in %s (pane %s), Revive%s", reason, strOr(v.Name, "-"), strOr(v.WorkspaceLabel, v.WorkspaceID), pane, cost),
			Ev:   with(about(pane), "why", "frozen", "reason", reason),
		})
	}

	// --- Weekly optimizer: once a new quota week is an hour old, Berthier
	// hears to have last week's Profile reviewed by an optimizer Division.
	if ws := WeekStart(float64(now), in.Usage.ResetsAt); in.Usage.ResetsAt != nil && in.Spend.LastWeekUSD > 0 && float64(now) >= ws+3600 {
		cands = append(cands, Escalation{
			Key:  fmt.Sprintf("optimize:%s", Num(ws)),
			Head: "[berthier-loop] Weekly Profile review:",
			Text: fmt.Sprintf("last week the Grand Army spent $%.2f, $%.2f of it on Waste signals. Spawn an optimizer Division of your own (berthier division-new optimizer --model opus \"...\") to study `berthier profile --week 1` and `berthier profile --week 1 --waste` and propose environment changes (CLAUDE.md, models per Division, skills, hooks, Brief habits) as an Artifact for the user's approval; it never applies them itself",
				in.Spend.LastWeekUSD, in.Spend.LastWeekWaste),
			Ev: Fields{"why": "optimize", "reason": "weekly Profile review", "usd": round2(in.Spend.LastWeekUSD)},
		})
	}
	events := []Escalation{}
	// Berthier's own trouble, and everything while Berthier is Frozen, goes to the user, not into a pane.
	toUser := func(c Escalation) bool {
		if berthier == nil {
			return false
		}
		if c.PaneID != nil && *c.PaneID == *berthier {
			return true
		}
		return frozen[*berthier] != nil && (c.To == nil || *c.To == *berthier)
	}
	for _, c := range cands {
		if prevEsc[c.Key] != 0 {
			continue
		}
		dest, target := "berthier", berthier
		if c.To != nil && *c.To != "" {
			dest, target = CorpsName(strOr(agents.M[*c.To].labelOrNil(), "")), c.To
		}
		if !toUser(c) && target != nil && held(*target) {
			continue // not marked escalated: the next tick's candidate tries again
		}
		c.Ev = with(c.Ev, "to", dest)
		events = append(events, c)
	}

	// --- Spoken alerts: the user is needed. Faster than escalation, because
	// they skip Berthier's judgment; deduplicated per episode like escalations.
	var speakAll []Speak
	for _, pane := range agents.Keys {
		v := agents.M[pane]
		age := now - v.StatusSince
		label := strOr(v.WorkspaceLabel, v.WorkspaceID)
		where := Spoken(label)
		if r, ok := div[CorpsName(strOr(v.WorkspaceLabel, ""))]; ok && r.Corps != "" {
			where += " in " + Spoken(r.Corps)
		}
		var why, text string
		since := v.StatusSince
		switch {
		case frozen[pane] != nil:
			if slices.Contains(unfreeze, pane) || autoPending[pane] {
				continue
			}
			why, text, since = "frozen", where+" is frozen: Revive or Fresh Start?", frozen[pane].Since
		case v.Status == "blocked" && age >= cfg.SpeakAfter:
			why, text = "blocked", where+" is waiting on you"
		case v.Status == "gone" && v.ReviveAttempts >= cfg.ReviveMax:
			why, text = "revive_failed", where+" crashed and could not be resumed"
		default:
			continue
		}
		speakAll = append(speakAll, Speak{fmt.Sprintf("speak_%s:%s:%d", why, pane, since), text, with(about(pane), "why", why, "to", "you")})
	}
	for _, w := range warn {
		corps := w.Ev["corps"].(string)
		speakAll = append(speakAll, Speak{"speak_" + w.Key,
			fmt.Sprintf("%s has used %d percent of its weekly budget", Spoken(corps), w.Ev["pct"]),
			Fields{"corps": corps, "why": "budget_warn", "to": "you"}})
	}
	speakAll = append(speakAll, quota.Speak...)
	for _, b := range blocks {
		speakAll = append(speakAll, Speak{"speak_" + b.key, Spoken(b.corps) + " is over its weekly budget",
			Fields{"corps": b.corps, "why": "budget_exhausted", "to": "you"}})
	}
	speak := []Speak{}
	for _, s := range speakAll {
		if prevEsc[s.Key] == 0 {
			speak = append(speak, s)
		}
	}

	// Berthier cannot escalate to itself: its own events go to the user instead.
	escalate, notify := []Escalation{}, []Escalation{}
	for _, e := range events {
		if toUser(e) {
			e.Ev = with(e.Ev, "to", "you")
			notify = append(notify, e)
		} else {
			escalate = append(escalate, e)
		}
	}

	escalated := map[string]int64{}
	for k, v := range prevEsc {
		if v > now-cfg.EscalationTTL {
			escalated[k] = v
		}
	}
	for _, e := range events {
		escalated[e.Key] = now
	}
	for _, s := range speak {
		escalated[s.Key] = now
	}
	for _, b := range blocks {
		escalated[b.key] = now
	}
	for _, k := range quota.Mark {
		escalated[k] = now
	}
	decided = append(decided, quota.Events...)

	// --- Corps: every workspace that is not a known Division and not Berthier's own.
	byWS := map[string][]string{}
	for _, k := range agents.Keys {
		a := agents.M[k]
		byWS[a.WorkspaceID] = append(byWS[a.WorkspaceID], k)
	}
	list := func(id string) []string {
		if l := byWS[id]; l != nil {
			return l
		}
		return []string{}
	}
	var divisionWS []Workspace
	for _, w := range workspaces {
		if div.IsDivision(w.Label) {
			divisionWS = append(divisionWS, w)
		}
	}
	unit := func(w Workspace) UnitState {
		u := UnitState{Name: w.Label, WorkspaceID: w.ID, Worktree: div[CorpsName(w.Label)].Worktree, Agents: list(w.ID)}
		if w.Worktree != nil {
			u.Path = w.Worktree.CheckoutPath
		}
		return u
	}
	corps := []Corps{}
	for _, w := range workspaces {
		if div.IsDivision(w.Label) || w.ID == berthierWS || w.Label == "berthier" {
			continue
		}
		c := Corps{Name: w.Label, WorkspaceID: w.ID, Agents: list(w.ID), Divisions: []UnitState{}}
		if w.Worktree != nil {
			c.RepoRoot = w.Worktree.RepoRoot
		}
		for _, d := range divisionWS {
			if div[CorpsName(d.Label)].Corps == w.Label {
				c.Divisions = append(c.Divisions, unit(d))
			}
		}
		corps = append(corps, c)
	}
	// Berthier's own Divisions (e.g. one coding Berthier itself) hang off Berthier.
	berthierDivisions := []UnitState{}
	var berthierLabel *string
	for _, w := range workspaces {
		if w.ID == berthierWS {
			berthierLabel = ptr(w.Label)
			break
		}
	}
	for _, d := range divisionWS {
		if berthierLabel != nil && div[CorpsName(d.Label)].Corps == *berthierLabel {
			berthierDivisions = append(berthierDivisions, unit(d))
		}
	}
	order := []string{}
	for _, w := range workspaces {
		order = append(order, w.ID)
	}

	// --- The Grand Army State: what was decided above, recorded.
	for _, k := range agents.Keys {
		if o := obs(k); o.Draft != nil && *o.Draft {
			if a := agents.M[k]; a.DraftAt == nil || now-*a.DraftAt > cfg.DraftQuiet {
				f := about(k)
				decided = append(decided, Event{"draft_held", fmt.Sprintf("holding prompts to %s in %s: its input box holds the user's draft", f["name"], k), f})
			}
			agents.M[k].DraftAt = ptr(now)
		}
	}
	for _, p := range slices.Sorted(maps.Keys(draftLook)) {
		look = append(look, Look{p, nil, []string{"draft"}})
	}
	for _, e := range limitRead {
		agents.M[e.pane].Limit = e.a.Limit
	}
	for _, k := range reviving {
		agents.M[k].ReviveAttempts++
		agents.M[k].LastReviveAt = ptr(now)
	}
	for _, r := range resume {
		a := agents.M[r.PaneID]
		if a.Limit != nil {
			l := *a.Limit
			l.Resumed = true
			a.Limit = &l
		}
		a.LimitResumedAt = ptr(now)
	}
	for _, f := range freezing {
		if a := agents.M[f.pane]; a != nil { // not given a Fresh Start by itself
			a.Frozen = frozen[f.pane]
		}
	}
	for _, k := range unfreeze {
		agents.M[k].Frozen = nil
	}
	for _, f := range freezing {
		ev := with(about(f.pane), "why", f.why, "context", f.cache.Context, "session", sessionOf(f.pane))
		decided = append(decided, Event{"frozen", fmt.Sprintf("froze %s in %s: due a %s with a cold cache (%s tokens, last call %dm ago); held for the user",
			ev["name"], f.pane, map[string]string{"crash": "Revive", "limit": "usage-limit resume"}[f.why], tokensK(f.cache.Context), minutes(now-f.cache.LastAt)), ev})
	}

	return TickOut{
		State: State{Version: 1, UpdatedAt: now, Berthier: berthier, Corps: corps, BerthierDivisions: berthierDivisions,
			Order: order, Agents: agents, Escalated: escalated, Interrupted: kept, AskLog: askLog},
		Look: look, Events: decided, Revive: revive, Rename: rename, Resume: resume, Proceed: proceed,
		Escalate: escalate, Notify: notify, Speak: speak, Deliver: deliver, Fresh: fresh, Thawed: thawed,
	}
}

func (l *Limit) resetAt() *int64 {
	if l == nil {
		return nil
	}
	return l.ResetAt
}

func (a *Agent) labelOrNil() *string {
	if a == nil {
		return nil
	}
	return a.WorkspaceLabel
}

func orI(p *int64, d int64) int64 {
	if p == nil {
		return d
	}
	return *p
}

func uniq(l []string) []string {
	slices.Sort(l)
	out := l[:0]
	for i, s := range l {
		if i == 0 || s != l[i-1] {
			out = append(out, s)
		}
	}
	return out
}

func orDefault(v, d int64) int64 {
	if v == 0 {
		return d
	}
	return v
}
