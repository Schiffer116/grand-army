package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"berthier/internal/core"
)

// ErrHerdrUnreachable is a tick that could not reach Herdr (e.g. not started
// yet after boot); the loop logs outages once per streak.
var ErrHerdrUnreachable = errors.New("herdr unreachable")

// Tick runs one Main Loop pass: observe Herdr, decide (core.Tick), act, write
// state. Only core.Tick writes the Grand Army State; this gathers the
// observations it asks for and carries out its actions.
func (e *Env) Tick() error {
	unlock, err := lockFile(filepath.Join(e.Home, "tick.lock"), false)
	if err != nil {
		return err
	}
	if unlock == nil {
		return nil // another tick is running
	}
	defer unlock()

	snap, err := e.Snapshot()
	if err != nil {
		return ErrHerdrUnreachable
	}
	prev := e.ReadState()
	now := e.now()
	inbox := e.ReadInbox()
	thaws := e.ReadThaws()
	// Fold new quota samples into the usage record the budgets are judged on.
	usage, err := e.UpdateUsage()
	if err != nil {
		usage = core.Usage{}
		readJSON(e.Usage, &usage)
	}
	budgets := e.ReadBudgets()
	divisions := e.ReadDivisions()
	spend := e.spendBrief(usage, now)
	decide := func(obs map[string]core.Obs) core.TickOut {
		return core.Tick(core.TickIn{Prev: prev, Snap: snap, Now: now, Cfg: e.CoreConfig(), Divisions: divisions,
			Inbox: inbox, Usage: usage, Budgets: budgets, Obs: obs, Spend: spend, Thaw: thaws})
	}

	// --- Observe what the decisions need: most ticks need nothing, else more
	// runs (a read can lead to a prompt, whose input box is read in turn).
	out := decide(nil)
	obs := map[string]core.Obs{}
	for pass := 0; len(out.Look) > 0 && pass < 3; pass++ {
		for _, l := range out.Look {
			obs[l.PaneID] = e.look(l, now, obs[l.PaneID])
		}
		out = decide(obs)
	}

	// --- Log the decisions, then carry out the actions and log their outcomes.
	for _, ev := range out.Events {
		e.Event(ev.Kind, ev.Msg, ev.Fields)
	}

	// --- Revive: resume the gone session in the pane it died in.
	for _, r := range out.Revive {
		args := []string{"agent", "start", r.Name, "--kind", "claude", "--pane", r.PaneID, "--timeout", fmt.Sprint(e.StartTimeoutMS), "--", "--resume", r.SessionID}
		label := ""
		if a := out.State.Agents.Get(r.PaneID); a != nil {
			label = strOr(a.WorkspaceLabel)
		}
		args = append(args, e.claudeFlags(label, e.ModelOf(label))...)
		// Herdr counts the start a success even if Claude exited at once
		// (e.g. "No conversation found"): the pane is back at its shell.
		if _, err := e.H.Run(args...); err == nil && !e.paneAtShell(r.PaneID) {
			e.Event("revived", fmt.Sprintf("revived %s in %s (session %s)", r.Name, r.PaneID, r.SessionID), r.Ev)
		} else {
			e.Event("revive_failed", fmt.Sprintf("revive failed: %s in %s (session %s)", r.Name, r.PaneID, r.SessionID), r.Ev)
		}
	}

	// --- Fresh Start: a new session in a Frozen Agent's pane, rebuilt from what is written down.
	for _, f := range out.Fresh {
		e.freshStart(f)
	}
	for _, id := range out.Thawed {
		os.Remove(filepath.Join(e.Thaws, id+".json"))
	}

	// --- Give agents back the names Herdr forgot (e.g. across a restart).
	for _, r := range out.Rename {
		if _, err := e.H.Run("agent", "rename", r.PaneID, r.Name); err == nil {
			e.Event("renamed", fmt.Sprintf("restored name %s in %s (session %s)", r.Name, r.PaneID, r.SessionID), r.Ev)
		} else {
			e.Event("rename_failed", fmt.Sprintf("name restore failed: %s in %s (session %s)", r.Name, r.PaneID, r.SessionID), r.Ev)
		}
	}

	// --- Usage limits: nudge an Agent on once its limit has reset.
	for _, r := range out.Resume {
		if err := e.H.PromptAgent(r.PaneID, core.Tag("[berthier resume]", e.LimitPrompt)); err == nil {
			e.Event("resumed", fmt.Sprintf("resumed %s in %s: usage limit reset", r.Name, r.PaneID), r.Ev)
		} else {
			e.Event("resume_failed", fmt.Sprintf("resume failed: %s in %s (usage limit reset): %s", r.Name, r.PaneID, err), with(r.Ev, "why", err.Error()))
		}
	}

	// --- Restart: nudge an Agent whose turn the restart cut off; core retries a failed nudge and logs giving up.
	for _, p := range out.Proceed {
		// A failed nudge is retried by later ticks (core keeps the session).
		if err := e.H.PromptAgent(p.PaneID, core.Tag("[berthier continue]", e.RestartPrompt)); err == nil {
			e.Event("continued", fmt.Sprintf("continued %s in %s: restart interrupted its turn", p.Name, p.PaneID), p.Ev)
		}
	}

	// --- Deliver asks; a delivered ask leaves the inbox.
	for _, d := range out.Deliver {
		if d.Wake {
			e.wake(d)
			continue
		}
		if err := e.H.PromptAgent(d.PaneID, d.Text); err == nil {
			os.Remove(filepath.Join(e.Inbox, d.ID+".json"))
			e.Event("delivered", fmt.Sprintf("delivered ask %s -> %s (%s)", d.From, d.To, d.PaneID), d.Ev)
		} else {
			e.Event("delivery_failed", fmt.Sprintf("ask delivery failed: %s -> %s (%s), ask %s: %s", d.From, d.To, d.PaneID, d.ID, err), with(d.Ev, "why", err.Error()))
		}
	}

	// --- Escalate: a Division's events go to its Corps's Main Agent, the rest
	// to Berthier, one prompt each. Fall back to the user if the target cannot
	// take it.
	e.escalate(out)

	// --- Berthier's own trouble goes straight to the user.
	for _, n := range out.Notify {
		e.H.Run("notification", "show", "Berthier needs you", "--body", n.Text, "--sound", "request")
		e.Event("notified", "notified user: "+n.Text, n.Ev)
	}

	// --- Say it out loud when the user is needed; one utterance per tick.
	if len(out.Speak) > 0 {
		var msg strings.Builder
		for _, s := range out.Speak {
			msg.WriteString(s.Text + ". ")
		}
		kind, said := "spoke", "spoke"
		if !e.speak(strings.TrimSuffix(msg.String(), " ")) {
			kind, said = "speak_failed", fmt.Sprintf("speak failed (%s)", e.Speak)
		}
		for _, s := range out.Speak {
			e.Event(kind, said+": "+s.Text+".", s.Ev)
		}
	}

	// --- Boards: new work for the Main Agents (after the rest, so a slow skipli never delays a Revive).
	e.pollBoards(out)

	return writeJSONAtomic(e.State, out.State, true)
}

// spendBrief is what the Main Loop needs of the Profile: this quota week's
// Waste signals and last week's Spend.
func (e *Env) spendBrief(u core.Usage, now int64) core.SpendBrief {
	start := core.WeekStart(float64(now), u.ResetsAt)
	var this, last []core.Turn
	for _, t := range e.LoadTurns() {
		switch {
		case t.Start >= start:
			this = append(this, t)
		case t.Start >= start-core.Week:
			last = append(last, t)
		}
	}
	b := core.SpendBrief{Waste: core.Waste(this)}
	for _, t := range last {
		b.LastWeekUSD += t.USD
	}
	for _, w := range core.Waste(last) {
		b.LastWeekWaste += w.USD
	}
	return b
}

func strOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// speak runs the notify-user speaker; false if it is missing or fails.
func (e *Env) speak(msg string) bool {
	if st, err := os.Stat(e.Speak); err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
		return false
	}
	return exec.Command(e.Speak, msg).Run() == nil
}

func (e *Env) escalate(out core.TickOut) {
	type key struct{ to, head string }
	groups := map[key][]core.Escalation{}
	for _, x := range out.Escalate {
		k := key{"-", x.Head}
		if x.To != nil {
			k.to = *x.To
		}
		groups[k] = append(groups[k], x)
	}
	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].to != keys[j].to {
			return keys[i].to < keys[j].to
		}
		return keys[i].head < keys[j].head
	})
	berthier := ""
	if out.State.Berthier != nil {
		berthier = *out.State.Berthier
	}
	for _, k := range keys {
		g := groups[k]
		who, _ := g[0].Ev["to"].(string)
		var target, msg string
		if k.to == "-" {
			target, msg = berthier, orStr(k.head, "[berthier-loop] Grand Army events:")
		} else {
			target, msg = k.to, orStr(k.head, "[berthier-loop] Events in your Divisions (you command them):")
		}
		for _, x := range g {
			msg += "\n- " + x.Text
		}
		if target != "" && e.H.PromptAgent(target, msg) == nil {
			for _, x := range g {
				e.Event("escalated", fmt.Sprintf("escalated %v to %s: %s", x.Ev["why"], who, x.Text), x.Ev)
			}
		} else {
			e.H.Run("notification", "show", "Berthier unreachable", "--body", msg, "--sound", "request")
			e.Event("unreachable", fmt.Sprintf("%s unreachable; notified user of %d event(s)", who, len(g)),
				core.Fields{"to": who, "events": len(g)})
		}
	}
}

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// look adds to o what a decision waits on from a pane. A pane that cannot be
// read for a limit stop is left unobserved: read next tick. Reads stay on
// screen (visible): more lines than that make Herdr scroll an idle Claude up
// and back to collect its history, and the user sees it jump.
func (e *Env) look(l core.Look, now int64, o core.Obs) core.Obs {
	sid := "-"
	if l.SessionID != nil {
		sid = *l.SessionID
	}
	for _, w := range l.Want {
		switch w {
		case "exited":
			out, err := e.H.Run("pane", "read", l.PaneID, "--source", "recent-unwrapped", "--lines", "20")
			b := err == nil && core.ExitedDeliberately(string(out), sid)
			o.Exited = &b
		case "no_transcript":
			b := !e.hasTranscript(sid)
			o.NoTranscript = &b
		case "at_shell":
			b := e.paneAtShell(l.PaneID)
			o.AtShell = &b
		case "interrupted":
			b := e.transcriptInterrupted(sid)
			o.Interrupted = &b
		case "cache":
			// Unknown (no transcript) is a zero Cache: small, so resumed as before.
			c := core.Cache{}
			if m := e.transcriptFiles(sid); len(m) > 0 {
				if c, _ = core.CacheOf(TailLines(m[0], 200)); c.LastAt == 0 {
					c, _ = core.CacheOf(TailLines(m[0], 5000))
				}
			}
			o.Cache = &c
		case "auto_fresh":
			o.Auto = e.autoCheck(l.PaneID)
		case "draft":
			// Unreadable counts as empty: the prompt itself then fails or not, as before.
			b, _ := e.H.HasDraft(l.PaneID)
			o.Draft = &b
		case "limit_reset":
			out, err := e.H.Run("pane", "read", l.PaneID, "--source", "visible")
			if err != nil {
				continue
			}
			o.LimitSeen = true
			if t, ok := core.UsageLimitReset(string(out), now, time.Local); ok {
				o.LimitReset = &t
			}
		}
	}
	return o
}

// transcriptInterrupted reads the end of a session's transcript; an unknown
// session counts as finished.
func (e *Env) transcriptInterrupted(sid string) bool {
	m := e.transcriptFiles(sid)
	if len(m) == 0 {
		return false
	}
	return core.TranscriptInterrupted(TailLines(m[0], 200))
}

func (e *Env) transcriptFiles(sid string) []string {
	m, _ := filepath.Glob(filepath.Join(e.ClaudeDir, "projects", "*", sid+".jsonl"))
	return m
}

// hasTranscript: Claude can only resume a session it saved.
func (e *Env) hasTranscript(sid string) bool { return len(e.transcriptFiles(sid)) > 0 }
