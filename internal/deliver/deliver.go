// Package deliver gets a prompt into an Agent and makes sure it took it. It
// sits above the Host seam: it only types and presses keys through the Host
// and reads screens through the Harness, so the logic is written once and
// tested against the fakes.
package deliver

import (
	"errors"
	"fmt"
	"time"

	"berthier/internal/core"
	"berthier/internal/harness"
	"berthier/internal/host"
)

// Delivery prompts the Agents of a Host. Poll is how often it checks that the
// Agent took what it was sent.
type Delivery struct {
	Host    host.Host
	Harness harness.Harness
	Poll    time.Duration
}

// ErrDraft is a prompt not sent because the Agent's input box holds text
// someone typed there (the user may be mid-dictation).
var ErrDraft = errors.New("its input box holds the user's draft (they may be typing); not sent, try again later")

// agent is the Agent in pane as the Host sees it; an error when there is none.
func (d Delivery) agent(pane string) (core.SnapAgent, error) {
	a, err := d.Host.Pane(pane)
	if err != nil || a.Status == "" || a.Agent == nil {
		return core.SnapAgent{}, fmt.Errorf("no agent in %s", pane)
	}
	return a, nil
}

func seq(a core.SnapAgent) int64 {
	if a.StateSeq == nil {
		return -1
	}
	return *a.StateSeq
}

func session(a core.SnapAgent) string {
	if a.Session == nil {
		return ""
	}
	return *a.Session
}

// HasDraft reports whether the input box in pane holds text someone typed;
// the Agent's own suggestion is not a draft.
func (d Delivery) HasDraft(pane string) (bool, error) {
	screen, err := d.Host.Read(pane, host.ReadOpts{ANSI: true})
	if err != nil {
		return false, err
	}
	return d.Harness.Draft(screen), nil
}

// submit presses the keys that send the input box.
func (d Delivery) submit(pane string) error {
	for _, k := range d.Harness.Keys(harness.Submit) {
		if err := d.Host.Key(pane, k); err != nil {
			return err
		}
	}
	return nil
}

// send types text and submits it.
func (d Delivery) send(pane, text string) error {
	if err := d.Host.Type(pane, text); err != nil {
		return err
	}
	return d.submit(pane)
}

// Prompt prompts the Agent in pane and makes sure it took it: typing the text
// and pressing Enter is not enough, since Claude swallows that Enter while it
// turns pasted image paths into attachments, leaving a draft. Taken once the
// Agent changes state, or, busy, its box is empty (queued). A draft gets
// Enter, a lost paste is sent again. The error says why. It never types into
// a box holding the user's draft: ErrDraft.
func (d Delivery) Prompt(pane, text string) error {
	before, err := d.agent(pane)
	if err != nil {
		return err
	}
	if draft, _ := d.HasDraft(pane); draft {
		return fmt.Errorf("%s: %w", pane, ErrDraft)
	}
	if err := d.send(pane, text); err != nil {
		return err
	}
	tries := 0
	for polls := 1; ; polls++ {
		time.Sleep(d.Poll)
		now, err := d.agent(pane)
		if err != nil {
			return fmt.Errorf("agent in %s went away", pane)
		}
		if seq(now) != seq(before) {
			return nil
		}
		if polls%4 != 0 {
			continue
		}
		screen, _ := d.Host.Read(pane, host.ReadOpts{})
		holds := d.Harness.PromptHeld(screen, text)
		if !holds && now.Status == "working" {
			return nil
		}
		if tries++; tries > 3 {
			return fmt.Errorf("Claude in %s did not take the prompt", pane)
		}
		if holds {
			d.submit(pane)
		} else {
			d.send(pane, text)
		}
	}
}

// Clear runs the Harness's clear command in pane and returns once the Agent is
// in a new session (its context is gone), the proof it took; a stuck draft or
// an unchanged session is an error. Like Prompt it never types into a box
// holding the user's draft.
func (d Delivery) Clear(pane string) error {
	before, err := d.agent(pane)
	if err != nil {
		return err
	}
	if draft, _ := d.HasDraft(pane); draft {
		return fmt.Errorf("%s: %w", pane, ErrDraft)
	}
	cmd := d.Harness.ClearCommand()
	if err := d.send(pane, cmd); err != nil {
		return err
	}
	for polls := 1; polls <= 40; polls++ {
		time.Sleep(d.Poll)
		now, err := d.agent(pane)
		if err != nil {
			return fmt.Errorf("agent in %s went away", pane)
		}
		if s := session(now); s != "" && s != session(before) {
			return nil
		}
		// The slash menu can hold the command until Enter.
		if polls%8 == 0 {
			if screen, _ := d.Host.Read(pane, host.ReadOpts{}); d.Harness.PromptHeld(screen, cmd) {
				d.submit(pane)
			}
		}
	}
	return fmt.Errorf("Claude in %s did not clear its context", pane)
}
