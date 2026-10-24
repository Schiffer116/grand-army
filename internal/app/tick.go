package app

import (
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"berthier/internal/core"
	"berthier/internal/host"
)

// ErrHostUnreachable is a tick that could not reach the Host (e.g. not started
// yet after boot); the loop logs outages once per streak.
var ErrHostUnreachable = host.ErrUnreachable

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

	prev := e.ReadState()
	now := e.now()
	e.noteRestart(prev, now)
	// After an outage (a tick that finds the Host unreachable, or a gap in the
	// state) the Host restarts the sessions it killed: give them time before
	// anything counts as gone.
	if prev.UpdatedAt != 0 && now-prev.UpdatedAt > e.CoreConfig().RestartGap {
		e.Restart.Store(now + int64(e.ExitGrace/time.Second))
	}
	snap, err := e.Snapshot()
	if err != nil {
		e.Restart.Store(now + int64(e.ExitGrace/time.Second))
		return ErrHostUnreachable
	}
	retired := e.RetiredNames()
	// What waits for a retired Corps stays on disk until it is reopened.
	inbox := slices.DeleteFunc(e.ReadInbox(), func(m core.Ask) bool { return slices.Contains(retired, core.CorpsName(m.To)) })
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
			Inbox: inbox, Usage: usage, Budgets: budgets, Obs: obs, Spend: spend, Thaw: thaws, Retired: retired})
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
		label := ""
		if a := out.State.Agents.Get(r.PaneID); a != nil {
			label = strOr(a.WorkspaceLabel)
		}
		opts := e.launchOpts(label, e.stickyModel(label, r.SessionID))
		opts.Resume = r.SessionID
		// A start that fails at once (e.g. "No conversation found") leaves the
		// pane back at its shell.
		if _, err := e.launch(r.PaneID, r.Name, opts); err == nil && !e.paneAtShell(r.PaneID) {
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
		if err := e.Host.RenamePane(r.PaneID, r.Name); err == nil {
			e.Event("renamed", fmt.Sprintf("restored name %s in %s (session %s)", r.Name, r.PaneID, r.SessionID), r.Ev)
		} else {
			e.Event("rename_failed", fmt.Sprintf("name restore failed: %s in %s (session %s)", r.Name, r.PaneID, r.SessionID), r.Ev)
		}
	}

	// --- Usage limits: nudge an Agent on once its limit has reset.
	for _, r := range out.Resume {
		if err := e.PromptAgent(r.PaneID, core.Tag("[berthier resume]", e.LimitPrompt)); err == nil {
			e.Event("resumed", fmt.Sprintf("resumed %s in %s: usage limit reset", r.Name, r.PaneID), r.Ev)
		} else {
			e.Event("resume_failed", fmt.Sprintf("resume failed: %s in %s (usage limit reset): %s", r.Name, r.PaneID, err), with(r.Ev, "why", err.Error()))
		}
	}

	// --- Restart: nudge an Agent whose turn the restart cut off; core retries a failed nudge and logs giving up.
	for _, p := range out.Proceed {
		// A failed nudge is retried by later ticks (core keeps the session).
		if err := e.PromptAgent(p.PaneID, core.Tag("[berthier continue]", e.RestartPrompt)); err == nil {
			e.Event("continued", fmt.Sprintf("continued %s in %s: restart interrupted its turn", p.Name, p.PaneID), p.Ev)
		}
	}

	// --- Deliver asks; a delivered ask leaves the inbox.
	for _, d := range out.Deliver {
		if d.Wake {
			e.wake(d)
			continue
		}
		if err := e.PromptAgent(d.PaneID, d.Text); err == nil {
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
		e.notify("Berthier needs you", n.Text)
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
	slices.SortFunc(keys, func(a, b key) int {
		return cmp.Or(cmp.Compare(a.to, b.to), cmp.Compare(a.head, b.head))
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
			target, msg = berthier, cmp.Or(k.head, "[berthier-loop] Grand Army events:")
		} else {
			target, msg = k.to, cmp.Or(k.head, "[berthier-loop] Events in your Divisions (you command them):")
		}
		for _, x := range g {
			msg += "\n- " + x.Text
		}
		if target != "" && e.PromptAgent(target, msg) == nil {
			for _, x := range g {
				e.Event("escalated", fmt.Sprintf("escalated %v to %s: %s", x.Ev["why"], who, x.Text), x.Ev)
			}
		} else {
			e.notify("Berthier unreachable", msg)
			e.Event("unreachable", fmt.Sprintf("%s unreachable; notified user of %d event(s)", who, len(g)),
				core.Fields{"to": who, "events": len(g)})
		}
	}
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
			// The session ended by its own hook, for a reason the user chose.
			r := e.exitOf(sid)
			b := r != nil && e.harness().DeliberateExit(r.Reason)
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
			c := e.transcriptCache(sid)
			o.Cache = &c
		case "auto_fresh":
			o.Auto = e.autoCheck(l.PaneID)
		case "draft":
			// Unreadable counts as empty: the prompt itself then fails or not, as before.
			b, _ := e.deliver().HasDraft(l.PaneID)
			o.Draft = &b
		case "limit_reset":
			out, err := e.Host.Read(l.PaneID, host.ReadOpts{})
			if err != nil {
				continue
			}
			o.LimitSeen = true
			if t, ok := e.harness().UsageLimit(out, now, time.Local); ok {
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
	return e.harness().Interrupted(TailLines(m[0], 200))
}

func (e *Env) transcriptFiles(sid string) []string { return e.harness().Transcripts(sid) }

// hasTranscript: Claude can only resume a session it saved.
func (e *Env) hasTranscript(sid string) bool { return len(e.transcriptFiles(sid)) > 0 }

// transcriptCache is session sid's prompt cache, read from its transcript.
// Unknown (no transcript) is a zero Cache: small, so resumed as before.
func (e *Env) transcriptCache(sid string) core.Cache {
	c := core.Cache{}
	if m := e.transcriptFiles(sid); len(m) > 0 {
		if c, _ = e.harness().CacheOf(TailLines(m[0], 200)); c.LastAt == 0 {
			c, _ = e.harness().CacheOf(TailLines(m[0], 5000))
		}
	}
	return c
}

// stickyModel is the model a restarted Agent of the workspace labelled label
// runs on: the one its session's transcript last called (a /model switch
// sticks), else the one recorded for its Division. A Division's record follows
// the transcript.
func (e *Env) stickyModel(label, session string) string {
	recorded := e.ModelOf(label)
	m := e.transcriptCache(session).Model
	if m == "" {
		return recorded
	}
	if d, ok := e.ReadDivisions()[core.CorpsName(label)]; ok && d.Model != m {
		d.Model = m
		e.SetDivision(core.CorpsName(label), &d)
	}
	return m
}
