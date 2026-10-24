package core

import (
	"slices"
	"strings"
)

// Army-wide broadcasts: one inbox message per Agent, delivered like a notify.
const (
	TagShutdown = "[berthier shutdown]"
	TagWake     = "[berthier wake]"
	TagQuota    = "[berthier quota]"
)

var broadcasts = map[string]struct{ tag, text string }{
	"shutdown": {TagShutdown, "The user is closing down soon: finish or park your current step, commit and push WIP, leave a `berthier report`, update `berthier notes`, and start nothing long."},
	"quota":    {TagQuota, "The account is nearly out of quota for this window: hand off briefly. Update `berthier notes` with what you were doing and the next step; no long write-up, start nothing new."},
	"wake":     {TagWake, "The army is back after a restart and your context was cleared: run `berthier notes`, read your Board, your last report and git status/log, then resume unfinished work."},
}

// IsBroadcast: an inbox kind that is an army-wide broadcast.
func IsBroadcast(kind string) bool { _, ok := broadcasts[kind]; return ok }

// BroadcastText is the prompt of a broadcast kind ("shutdown", "wake"): its
// tag and standing text, then the user's extra text capped at max runes
// (<= 0: uncapped). ok is false for an unknown kind.
func BroadcastText(kind, extra string, max int) (text string, ok bool) {
	b, ok := broadcasts[kind]
	if !ok {
		return "", false
	}
	extra = strings.TrimSpace(extra)
	if r := []rune(extra); max > 0 && len(r) > max {
		extra = string(r[:max]) + " [truncated]"
	}
	if extra != "" {
		return b.tag + " " + b.text + " " + extra, true
	}
	return b.tag + " " + b.text, true
}

// BroadcastTargets are the addressee names (inbox `to`) of every Agent with a
// live pane, Berthier included, and how many Frozen Agents are skipped.
func BroadcastTargets(ag Agents, berthier *string) (to []string, skipped int) {
	for _, k := range ag.Keys {
		a := ag.M[k]
		switch {
		case a.Frozen != nil:
			skipped++
		case a.Status == "gone":
		default:
			if n := targetName(k, a, berthier); n != "" {
				to = append(to, n)
			}
		}
	}
	return to, skipped
}

func targetName(pane string, a *Agent, berthier *string) string {
	if berthier != nil && pane == *berthier {
		return "berthier"
	}
	return CorpsName(strOr(a.WorkspaceLabel, ""))
}

// Working: an Agent in this status had work going, as against idle or done.
func Working(status string) bool { return status != "idle" && status != "done" }

// WorkingNames are the BroadcastTargets that have work going: what a Shutdown
// snapshots, for a Wake to wake only them.
func WorkingNames(ag Agents, berthier *string) []string {
	var out []string
	for _, k := range ag.Keys {
		a := ag.M[k]
		if a.Frozen != nil || a.Status == "gone" || !Working(a.Status) {
			continue
		}
		if n := targetName(k, a, berthier); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// WakeSplit divides the addressees of a Wake into those that had work going
// (awake) and the rest (idle), and lists the working ones that have no Agent
// to wake now (absent).
func WakeSplit(to, working []string) (awake, idle, absent []string) {
	for _, n := range to {
		if slices.Contains(working, n) {
			awake = append(awake, n)
		} else {
			idle = append(idle, n)
		}
	}
	for _, n := range working {
		if !slices.Contains(to, n) && !slices.Contains(absent, n) {
			absent = append(absent, n)
		}
	}
	return awake, idle, absent
}
