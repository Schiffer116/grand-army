package core

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The Grand Army view model: Grand Army State + Agents' reports + inbox +
// loop.log, joined for the bar, the desktop widget and the Console. Pure.

// Report is an Agent's one-line status (state/reports/<session>.json).
type Report struct {
	Line string `json:"line"`
	At   *int64 `json:"at"`
}

// Demo is a place an Agent reports its demo or preview lives
// (state/demos/<session>.json holds a newest-first list).
type Demo struct {
	URL   string `json:"url"`
	Label string `json:"label"`
	At    int64  `json:"at"`
}

// ArmyIn is everything the view is joined from.
type ArmyIn struct {
	State      State
	Reports    map[string]Report
	Demos      map[string][]Demo // session id -> its Agent's demos, newest first
	Inbox      []Ask
	Costs      map[string]float64 // session id -> dollars (state/usage.json)
	Divisions  Divisions
	Log        []string // recent loop.log lines
	Usage      Usage
	Budgets    map[string]float64
	BudgetWarn float64
	FreshEst   int64 // tokens a Fresh Start loads
	Available  bool
	Now        int64
	Interval   float64 // Main Loop seconds
	ReviveMax  int
}

type AgentView struct {
	PaneID         string      `json:"pane_id"`
	Name           *string     `json:"name"`
	Kind           *string     `json:"kind"`
	TabID          *string     `json:"tab_id"`
	Status         *string     `json:"status"`
	StatusSince    *int64      `json:"status_since"`
	PrevStatus     *string     `json:"prev_status"`
	SessionID      *string     `json:"session_id"`
	Title          *string     `json:"title"`
	Cwd            *string     `json:"cwd"`
	ReviveAttempts int         `json:"revive_attempts"`
	LastReviveAt   *int64      `json:"last_revive_at"`
	Who            string      `json:"who"`
	Cost           *float64    `json:"cost"`
	DivisionModel  *string     `json:"division_model"`
	NeedsYou       bool        `json:"needs_you"`
	Report         *string     `json:"report"`
	ReportAt       *int64      `json:"report_at"`
	Demos          []Demo      `json:"demos"`
	Frozen         *FrozenView `json:"frozen"`
}

// FrozenView is a Frozen Agent, for the user's choice: what a Revive would
// cost (ReviveUSD nil: model unpriced).
type FrozenView struct {
	Frozen
	ReviveUSD *float64 `json:"revive_usd"`
	// What a Fresh Start loads (Notes, Board, last report, git), estimated.
	FreshTokens int64    `json:"fresh_tokens"`
	FreshUSD    *float64 `json:"fresh_usd"`
}

type DivisionView struct {
	Name        string      `json:"name"`
	WorkspaceID string      `json:"workspace_id"`
	Agents      []AgentView `json:"agents"`
	Worktree    bool        `json:"worktree"`
}

type CorpsView struct {
	Name        string         `json:"name"`
	WorkspaceID string         `json:"workspace_id"`
	Agents      []AgentView    `json:"agents"`
	Divisions   []DivisionView `json:"divisions"`
}

type UnitView struct {
	Kind        string         `json:"kind"`
	Name        string         `json:"name"`
	WorkspaceID string         `json:"workspace_id"`
	Agents      []AgentView    `json:"agents"`
	Divisions   []DivisionView `json:"divisions"`
}

type Counts struct {
	Working int `json:"working"`
	Blocked int `json:"blocked"`
	Done    int `json:"done"`
	Idle    int `json:"idle"`
	Unknown int `json:"unknown"`
	Gone    int `json:"gone"`
	Frozen  int `json:"frozen"` // also counted by status (gone, or idle on a usage limit)
}

type BudgetView struct {
	UsageView
	Warn float64 `json:"warn"`
}

type LoopView struct {
	Interval  float64 `json:"interval"`
	ReviveMax int     `json:"revive_max"`
	StartedAt any     `json:"started_at"`
}

// LogEntry is a loop.log event ({ts, kind, msg, ...fields}; see Event) with
// its Console category (tag) and display label. Fields beyond those are kept.
type LogEntry map[string]any

type EscalationView struct {
	At    any    `json:"at"`
	Kind  any    `json:"kind"`
	Label string `json:"label"`
	To    any    `json:"to"`
	Who   any    `json:"who"`
	Why   any    `json:"why"`
}

// ArmyView is `berthier army`'s output.
type ArmyView struct {
	Available         bool             `json:"available"`
	UpdatedAt         *int64           `json:"updated_at"`
	Loop              LoopView         `json:"loop"`
	Budget            BudgetView       `json:"budget"`
	Counts            Counts           `json:"counts"`
	NeedsYou          []AgentView      `json:"needs_you"`
	Berthier          *AgentView       `json:"berthier"`
	Corps             []CorpsView      `json:"corps"`
	BerthierDivisions []DivisionView   `json:"berthier_divisions"`
	Units             []UnitView       `json:"units"`
	Asks              []Ask            `json:"asks"`
	AskLog            []AskLogEntry    `json:"ask_log"`
	Escalations       []EscalationView `json:"escalations"`
	Activity          []LogEntry       `json:"activity"`
}

var logTags = map[string]string{
	"loop_started": "loop", "loop_stopped": "loop", "loop_recovered": "loop", "loop_error": "error",
	"revived": "revive", "revive_failed": "revive", "revive_skipped": "revive", "forgot": "forget",
	"spoke": "speak", "speak_failed": "speak", "escalated": "escal", "notified": "escal", "unreachable": "escal",
	"delivered": "ask", "delivery_failed": "ask",
	"adopted": "army", "brief_sent": "army", "brief_failed": "army", "renamed": "army", "rename_failed": "army",
	"frozen": "revive", "fresh_started": "revive", "fresh_failed": "revive", "thaw_ignored": "revive",
	"limit_stop": "limit", "resumed": "limit", "resume_failed": "limit", "continued": "revive", "continue_failed": "revive", "budget_exhausted": "budget",
}

func logEntry(m map[string]any) LogEntry {
	kind, _ := m["kind"].(string)
	tag := logTags[kind]
	if tag == "" {
		tag = "other"
	}
	label := tag
	if tag == "other" {
		label = "info"
	}
	e := LogEntry{}
	for k, v := range m {
		e[k] = v
	}
	e["at"], e["tag"], e["text"], e["label"] = m["ts"], tag, m["msg"], label
	return e
}

var escalationLabels = map[string]string{
	"revive_failed": "revive failed", "division_stopped": "stopped", "long": "long run",
	"ask_stuck": "ask stuck", "budget_warn": "budget", "frozen": "frozen",
}

func truthy(v any) bool { return v != nil && v != false }

func show(v any) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprint(v)
}

// escalationWho is who an escalation is about, as the Console shows it:
// "dark-mode d:p1", "dotfiles ⇄ skipli" (asks bouncing), "skipli → notes".
func escalationWho(e LogEntry) any {
	switch {
	case truthy(e["ask_from"]):
		arrow := "→"
		if e["why"] == "pingpong" {
			arrow = "⇄"
		}
		return fmt.Sprintf("%s %s %s", show(e["ask_from"]), arrow, show(e["ask_to"]))
	case truthy(e["pane"]):
		return fmt.Sprintf("%s %s", show(e["corps"]), show(e["pane"]))
	case e["why"] == "budget_warn":
		return e["corps"]
	}
	return nil
}

func atNum(e LogEntry) (float64, bool) {
	n, ok := e["at"].(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}

var (
	needsRe    = regexp.MustCompile(`(?i)^needs you:`)
	needsStrip = regexp.MustCompile(`(?i)^needs you: *`)
)

// Army joins the Grand Army into its view model.
func Army(in ArmyIn) ArmyView {
	st := &in.State
	berthier := deref(st.Berthier)
	agentView := func(pane string) AgentView {
		a := st.Agents.Get(pane)
		if a == nil {
			a = &Agent{}
		}
		v := AgentView{PaneID: pane, Name: a.Name, Kind: a.Kind, TabID: a.TabID, PrevStatus: a.PrevStatus,
			SessionID: a.SessionID, Title: a.Title, Cwd: a.Cwd, ReviveAttempts: a.ReviveAttempts, LastReviveAt: a.LastReviveAt}
		if a.Status != "" {
			v.Status, v.StatusSince = ptr(a.Status), ptr(a.StatusSince)
		}
		v.Who = "berthier"
		if st.Berthier == nil || pane != berthier {
			v.Who = CorpsName(deref(a.WorkspaceLabel))
		}
		if c, ok := in.Costs[deref(a.SessionID)]; ok {
			v.Cost = &c
		}
		if m := in.Divisions[v.Who].Model; m != "" {
			v.DivisionModel = &m
		}
		var line *string
		if r, ok := in.Reports[deref(a.SessionID)]; ok {
			line, v.ReportAt = &r.Line, r.At
		}
		v.Demos = append([]Demo{}, in.Demos[deref(a.SessionID)]...)
		if a.Frozen != nil {
			v.Frozen = &FrozenView{Frozen: *a.Frozen}
			if usd, ok := a.Frozen.ReviveUSD(); ok {
				v.Frozen.ReviveUSD = &usd
			}
			v.Frozen.FreshTokens = in.FreshEst
			if usd, ok := a.Frozen.WriteUSD(in.FreshEst); ok {
				v.Frozen.FreshUSD = &usd
			}
		}
		v.NeedsYou = (line != nil && needsRe.MatchString(*line)) || a.Status == "blocked" || a.Frozen != nil
		if line != nil {
			v.Report = ptr(needsStrip.ReplaceAllString(*line, ""))
		}
		return v
	}
	agentViews := func(panes []string) []AgentView {
		out := []AgentView{}
		for _, p := range panes {
			out = append(out, agentView(p))
		}
		return out
	}
	divViews := func(l []UnitState) []DivisionView {
		out := []DivisionView{}
		for _, d := range l {
			out = append(out, DivisionView{CorpsName(d.Name), d.WorkspaceID, agentViews(d.Agents), d.Worktree})
		}
		return out
	}
	// Herdr's workspace order: Corps and Berthier as Herdr lists them, each
	// unit's Divisions under it in the same order.
	pos := map[string]int{}
	for i, id := range st.Order {
		pos[id] = i
	}
	posOf := func(id string) int {
		if p, ok := pos[id]; ok {
			return p
		}
		return 1 << 30
	}
	sortDivs := func(d []DivisionView) []DivisionView {
		slices.SortStableFunc(d, func(a, b DivisionView) int { return cmp.Compare(posOf(a.WorkspaceID), posOf(b.WorkspaceID)) })
		return d
	}
	corps := []CorpsView{}
	for _, c := range st.Corps {
		corps = append(corps, CorpsView{CorpsName(c.Name), c.WorkspaceID, agentViews(c.Agents), sortDivs(divViews(c.Divisions))})
	}
	bdivs := sortDivs(divViews(st.BerthierDivisions))
	units := []UnitView{}
	for _, c := range corps {
		units = append(units, UnitView{"corps", c.Name, c.WorkspaceID, c.Agents, c.Divisions})
	}
	var bv *AgentView
	if st.Berthier != nil {
		v := agentView(berthier)
		bv = &v
		ws := ""
		if a := st.Agents.Get(berthier); a != nil {
			ws = a.WorkspaceID
		}
		units = append(units, UnitView{"berthier", "berthier", ws, []AgentView{v}, bdivs})
	}
	slices.SortStableFunc(units, func(a, b UnitView) int { return cmp.Compare(posOf(a.WorkspaceID), posOf(b.WorkspaceID)) })

	var all []AgentView
	for _, c := range corps {
		all = append(all, c.Agents...)
		for _, d := range c.Divisions {
			all = append(all, d.Agents...)
		}
	}
	if bv != nil {
		all = append(all, *bv)
	}
	for _, d := range bdivs {
		all = append(all, d.Agents...)
	}
	needs := []AgentView{}
	for _, a := range all {
		if a.NeedsYou {
			needs = append(needs, a)
		}
	}
	slices.SortStableFunc(needs, func(a, b AgentView) int {
		return cmp.Or(cmp.Compare(orI(a.StatusSince, 0), orI(b.StatusSince, 0)), cmp.Compare(a.Who, b.Who))
	})

	var counts Counts
	for _, k := range st.Agents.Keys {
		switch st.Agents.M[k].Status {
		case "working":
			counts.Working++
		case "blocked":
			counts.Blocked++
		case "done":
			counts.Done++
		case "idle":
			counts.Idle++
		case "unknown":
			counts.Unknown++
		case "gone":
			counts.Gone++
		}
		if st.Agents.M[k].Frozen != nil {
			counts.Frozen++
		}
	}

	// Lines that are not events (a stray prose line) are skipped.
	var entries []LogEntry
	for _, l := range in.Log {
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader([]byte(l)))
		dec.UseNumber()
		if dec.Decode(&m) == nil && m != nil {
			entries = append(entries, logEntry(m))
		}
	}
	var started any
	for _, e := range entries {
		if e["kind"] == "loop_started" {
			started = e["at"]
		}
	}
	escalations := []EscalationView{}
	for _, e := range entries {
		at, ok := atNum(e)
		if !ok || at < float64(in.Now-7*86400) {
			continue
		}
		switch e["kind"] {
		case "spoke":
			why, _ := e["msg"].(string)
			escalations = append(escalations, EscalationView{e["at"], "speak", "speak", "you", e["corps"], strings.TrimPrefix(why, "spoke: ")})
		case "escalated", "notified":
			why, _ := e["why"].(string)
			label := escalationLabels[why]
			if label == "" {
				label = why
			}
			escalations = append(escalations, EscalationView{e["at"], e["why"], label, e["to"], escalationWho(e), e["reason"]})
		}
	}
	reverse(escalations)
	tail := entries
	if len(tail) > 200 {
		tail = tail[len(tail)-200:]
	}
	activity := append([]LogEntry{}, tail...)
	reverse(activity)

	asks := append([]Ask{}, in.Inbox...)
	slices.SortStableFunc(asks, func(a, b Ask) int { return cmp.Compare(a.At, b.At) })
	askLog := append([]AskLogEntry{}, st.AskLog...)
	slices.SortStableFunc(askLog, func(a, b AskLogEntry) int { return cmp.Compare(a.At, b.At) })
	reverse(askLog)

	var updated *int64
	if st.UpdatedAt != 0 {
		updated = ptr(st.UpdatedAt)
	}
	return ArmyView{
		Available: in.Available, UpdatedAt: updated,
		Loop:   LoopView{in.Interval, in.ReviveMax, started},
		Budget: BudgetView{in.Usage.View(in.Budgets, in.Now), in.BudgetWarn},
		Counts: counts, NeedsYou: needs, Berthier: bv, Corps: corps, BerthierDivisions: bdivs, Units: units,
		Asks: asks, AskLog: askLog, Escalations: escalations, Activity: activity,
	}
}

func reverse[T any](l []T) {
	for i, j := 0, len(l)-1; i < j; i, j = i+1, j-1 {
		l[i], l[j] = l[j], l[i]
	}
}
