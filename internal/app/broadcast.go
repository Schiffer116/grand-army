package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"berthier/internal/core"
)

// --- shutdown, wake

// wakeSnap is who had work going when the army went down: Shutdown writes it
// before it broadcasts, a restart gap writes it from the state of the Agents
// before the gap, and Wake reads it (once) to wake only them.
type wakeSnap struct {
	At      int64    `json:"at"`
	From    string   `json:"from"` // "shutdown" or "restart"
	Working []string `json:"working"`
}

// shutdownSnapAge is how long a Shutdown snapshot is joined by a second
// Shutdown's (the Agents it woke to wrap up are not "working" at their own).
const shutdownSnapAge = 3600

func (e *Env) wakeFile() string { return filepath.Join(e.Home, "wake.json") }

func (e *Env) readWakeSnap() (w wakeSnap, ok bool) { return w, readJSON(e.wakeFile(), &w) }

// noteRestart records who had work going in the state before a restart gap
// (the Main Loop was down: a crash, no Shutdown), unless a Shutdown's snapshot
// is waiting for its Wake.
func (e *Env) noteRestart(prev core.State, now int64) {
	if prev.UpdatedAt == 0 || now-prev.UpdatedAt < e.RestartGap {
		return
	}
	if w, ok := e.readWakeSnap(); ok && w.From == "shutdown" {
		return
	}
	names := core.WorkingNames(prev.Agents, prev.Berthier)
	writeJSONAtomic(e.wakeFile(), wakeSnap{now, "restart", names}, false)
}

// BroadcastResult is what a Shutdown or Wake queued.
type BroadcastResult struct {
	Sent   []string // addressees it went to
	Idle   []string // a Wake left alone: idle at Shutdown
	Absent []string // a Wake found no Agent for: had work going
	Frozen int      // skipped, both kinds
	Source string   // what a Wake went by: "shutdown", "restart" or "state"
}

// Broadcast queues an army-wide `shutdown` or `wake` (extra: the user's own
// words, capped like notify) as one one-way inbox file per Agent with a live
// pane, Frozen ones skipped; the loop delivers each when its Agent is idle.
// A Shutdown first snapshots who is working; a Wake goes only to them.
func (e *Env) Broadcast(kind, extra string) (sent, skipped int, err error) {
	r, err := e.BroadcastWho(kind, extra)
	return len(r.Sent), r.Frozen + len(r.Idle), err
}

// BroadcastWho is Broadcast with the names it went to and skipped.
func (e *Env) BroadcastWho(kind, extra string) (BroadcastResult, error) {
	var r BroadcastResult
	if st, err := os.Stat(e.State); err != nil || st.Size() == 0 {
		return r, fmt.Errorf("no Grand Army State yet; is berthier-loop running?")
	}
	max := 2000
	if n, err := strconv.Atoi(e.getenv("BERTHIER_NOTIFY_MAX")); err == nil {
		max = n
	}
	text, ok := core.BroadcastText(kind, extra, max)
	if !ok {
		return r, fmt.Errorf("unknown broadcast %q", kind)
	}
	st := e.ReadState()
	to, frozen := core.BroadcastTargets(st.Agents, st.Berthier)
	r.Frozen = frozen
	now := e.now()
	switch kind {
	case "shutdown":
		snap := wakeSnap{now, "shutdown", core.WorkingNames(st.Agents, st.Berthier)}
		if old, ok := e.readWakeSnap(); ok && old.From == "shutdown" && now-old.At < shutdownSnapAge {
			for _, n := range old.Working {
				if !slices.Contains(snap.Working, n) {
					snap.Working = append(snap.Working, n)
				}
			}
		}
		if err := writeJSONAtomic(e.wakeFile(), snap, false); err != nil {
			return r, err
		}
		r.Sent = to
	case "wake":
		snap, ok := e.readWakeSnap()
		r.Source = snap.From
		if !ok {
			r.Source, snap.Working = "state", core.WorkingNames(st.Agents, st.Berthier)
		}
		r.Sent, r.Idle, r.Absent = core.WakeSplit(to, snap.Working)
	}
	stamp := e.Now().UnixNano()
	for i, name := range r.Sent {
		b, _ := json.Marshal(struct {
			Kind string `json:"kind"`
			From string `json:"from"`
			To   string `json:"to"`
			At   int64  `json:"at"`
			Text string `json:"text"`
		}{kind, "service", name, now, text})
		if err := writeAtomic(filepath.Join(e.Inbox, fmt.Sprintf("%d-%s-%d.json", stamp, kind, i)), append(b, '\n')); err != nil {
			return r, err
		}
	}
	if kind == "wake" {
		os.Remove(e.wakeFile()) // a Wake answers one Shutdown or restart
	}
	return r, nil
}

// cmdBroadcast is `berthier shutdown [text]` / `berthier wake`.
func cmdBroadcast(kind string) command {
	return func(e *Env, args []string, io IO) error {
		if len(args) > 1 || (kind == "wake" && len(args) > 0) {
			if kind == "wake" {
				return die("usage: berthier wake")
			}
			return die(`usage: berthier shutdown ["<text>"]`)
		}
		extra := ""
		if len(args) == 1 {
			extra = args[0]
		}
		r, err := e.BroadcastWho(kind, extra)
		if err != nil {
			return die("%v", err)
		}
		fmt.Fprintf(io.Out, "%s: queued for %d Agents; %d Frozen skipped; each gets it when idle\n", kind, len(r.Sent), r.Frozen)
		if kind == "wake" {
			fmt.Fprintf(io.Out, "woken (had work going, by %s): %s\n", r.Source, nameList(r.Sent))
			fmt.Fprintf(io.Out, "skipped (idle, left untouched): %s\n", nameList(r.Idle))
			if len(r.Absent) > 0 {
				fmt.Fprintf(io.Out, "not running (had work going): %s\n", nameList(r.Absent))
			}
		}
		return nil
	}
}

func nameList(l []string) string {
	if len(l) == 0 {
		return "-"
	}
	return strings.Join(l, ", ")
}
