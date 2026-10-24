package deliver

import (
	"errors"
	"strings"
	"testing"
	"time"

	"berthier/internal/agentsim"
	"berthier/internal/claude/claudetest"
	"berthier/internal/host"
	"berthier/internal/host/hosttest"
)

// rig is one Agent, taking prompts the way mode says (see agentsim), in a pane
// of a hosttest.Fake. Mode "nostart" has no Agent in the pane.
type rig struct {
	f    *hosttest.Fake
	sim  *agentsim.Sim
	pane string
}

func newRig(t *testing.T, mode string) rig {
	f := hosttest.New()
	c, err := f.CreateWorkspace(t.TempDir(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	sim := agentsim.New(f, mode)
	if mode != "nostart" {
		sim.Attach(c.PaneID)
	}
	return rig{f, sim, c.PaneID}
}

func (r rig) delivery() Delivery {
	return Delivery{Host: r.f, Harness: claudetest.Fake{}, Poll: time.Microsecond}
}

// count is how many calls of method the Host got.
func (r rig) count(method string) int { return len(r.f.CallsTo(method)) }

func (r rig) keys(k host.Key) int {
	n := 0
	for _, c := range r.f.CallsTo("Key") {
		if c.Args[1] == string(k) {
			n++
		}
	}
	return n
}

const ask = "Look at /tmp/a.png and reply"

// Every way an Agent can take, hold, lose or refuse a prompt.
func TestPrompt(t *testing.T) {
	for _, c := range []struct {
		mode, err    string
		types, enter int
		taken        string
	}{
		{"takes", "", 1, 1, ask},
		{"draft", "", 1, 2, ask},
		{"lost", "", 2, 2, ask},
		{"busy", "", 1, 1, ask},
		{"never", "Claude in w1:p1 did not take the prompt", 1, 4, ""},
		{"error", "agent target x not found", 1, 0, ""},
		{"typing", "w1:p1: " + ErrDraft.Error(), 0, 0, ""},
		{"nostart", "no agent in w1:p1", 0, 0, ""},
	} {
		r := newRig(t, c.mode)
		got := ""
		if err := r.delivery().Prompt(r.pane, ask); err != nil {
			got = err.Error()
		}
		if got != c.err {
			t.Errorf("%s: error %q, want %q", c.mode, got, c.err)
		}
		if n := r.count("Type"); n != c.types {
			t.Errorf("%s: %d Type calls, want %d", c.mode, n, c.types)
		}
		if n := r.keys(host.KeyEnter); n != c.enter {
			t.Errorf("%s: %d Enter presses, want %d", c.mode, n, c.enter)
		}
		if took := r.sim.Took(r.pane); took != c.taken {
			t.Errorf("%s: the Agent took %q, want %q", c.mode, took, c.taken)
		}
	}
	r := newRig(t, "typing")
	if err := r.delivery().Prompt(r.pane, ask); !errors.Is(err, ErrDraft) {
		t.Errorf("a draft is %v, not ErrDraft", err)
	}
}

// A multi-line prompt is typed whole, once.
func TestPromptKeepsNewlines(t *testing.T) {
	r := newRig(t, "takes")
	text := "Build it.\nThen report; $(touch /tmp/x) 'quoted'"
	if err := r.delivery().Prompt(r.pane, text); err != nil {
		t.Fatal(err)
	}
	if got := r.f.CallsTo("Type"); len(got) != 1 || got[0].Args[1] != text {
		t.Errorf("Type calls %v", got)
	}
	if got := r.sim.Took(r.pane); got != text {
		t.Errorf("took %q", got)
	}
}

func TestHasDraft(t *testing.T) {
	for mode, want := range map[string]bool{"takes": false, "typing": true} {
		r := newRig(t, mode)
		if got, err := r.delivery().HasDraft(r.pane); err != nil || got != want {
			t.Errorf("%s: HasDraft = %v, %v", mode, got, err)
		}
	}
	r := newRig(t, "takes")
	if _, err := r.delivery().HasDraft("nope:p1"); !errors.Is(err, host.ErrNotFound) {
		t.Errorf("HasDraft of no pane: %v", err)
	}
}

func TestClear(t *testing.T) {
	for _, c := range []struct{ mode, err string }{
		{"takes", ""},
		{"noclear", "Claude in w1:p1 did not clear its context"},
		{"typing", "w1:p1: " + ErrDraft.Error()},
		{"nostart", "no agent in w1:p1"},
	} {
		r := newRig(t, c.mode)
		got := ""
		if err := r.delivery().Clear(r.pane); err != nil {
			got = err.Error()
		}
		if got != c.err {
			t.Errorf("%s: error %q, want %q", c.mode, got, c.err)
		}
		if c.mode == "takes" {
			if sid, took := r.sim.Session(r.pane), r.sim.Took(r.pane); sid != "s2" || !strings.HasPrefix(took, "/clear") {
				t.Errorf("session %s after the clear took %q", sid, took)
			}
		}
	}
}
