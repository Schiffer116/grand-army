package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"berthier/internal/claude"
	"berthier/internal/core"
	"berthier/internal/deliver"
	"berthier/internal/harness"
)

// The Host and Harness ports (ADR 0006), as Env uses them.

// harness is the Harness the Agents run: Claude Code unless a test set one.
func (e *Env) harness() harness.Harness {
	if e.Harness != nil {
		return e.Harness
	}
	return claude.Claude{ConfigDir: e.ClaudeDir}
}

// deliver is how prompts reach Agents.
func (e *Env) deliver() deliver.Delivery {
	return deliver.Delivery{Host: e.Host, Harness: e.harness(), Poll: e.PromptPoll}
}

// PromptAgent prompts the Agent in pane and makes sure it took it
// (deliver.Delivery.Prompt); never type into a pane any other way.
func (e *Env) PromptAgent(pane, text string) error { return e.deliver().Prompt(pane, text) }

// ClearAgent runs the Harness's clear command in pane (deliver.Delivery.Clear).
func (e *Env) ClearAgent(pane string) error { return e.deliver().Clear(pane) }

// Workspaces lists the Host's workspaces.
func (e *Env) Workspaces() ([]core.Workspace, error) { return e.Host.Workspaces() }

// Snapshot is the Host's view of every workspace and pane.
func (e *Env) Snapshot() (core.Snapshot, error) { return e.Host.Snapshot() }

// notify shows the user a notification in the Host.
func (e *Env) notify(title, body string) { e.Host.Notify(title, body) }

// launchOpts is how Claude starts in the workspace labelled label: its model,
// and what its role allows. A Division's Agent may not use Artifact unless
// division-new --artifact. A Main Agent (not a Division) with no recorded
// model runs on CorpsModel. Berthier's persona comes from the SessionStart
// hook (`berthier session-start`), not from a flag.
func (e *Env) launchOpts(label, model string) harness.LaunchOpts {
	o := harness.LaunchOpts{Model: model}
	d, isDivision := e.ReadDivisions()[core.CorpsName(label)]
	if model == "" && !isDivision {
		o.Model = e.CorpsModel
	}
	if isDivision && !d.Artifact {
		o.Disallow = []string{"Artifact"}
	}
	return o
}

// errExited is a launch whose process ended before the Host saw an Agent.
var errExited = errors.New("it exited at once")

// launch starts the Harness in pane (at a shell prompt) and waits until the
// Host lists an Agent there, then names it. It returns the Agent's status
// ("blocked" behind a startup dialog). A process that ends before an Agent
// appears (e.g. "No conversation found" on a resume) is errExited; none after
// StartTimeoutMS is an error.
func (e *Env) launch(pane, name string, o harness.LaunchOpts) (string, error) {
	if err := e.Host.Start(pane, e.harness().Launch(o)); err != nil {
		return "", err
	}
	deadline := time.Now().Add(time.Duration(e.StartTimeoutMS) * time.Millisecond)
	ran := false
	for {
		a, err := e.Host.Pane(pane)
		if err != nil {
			return "", err
		}
		if a.Agent != nil && a.Status != "" && (a.Status != "unknown" || !time.Now().Before(deadline)) {
			if err := e.Host.RenamePane(pane, name); err != nil {
				return "", err
			}
			return a.Status, nil
		}
		if p, err := e.Host.Process(pane); err == nil {
			ran = ran || p.Alive()
			if ran && p.AtShell {
				return "", errExited
			}
		}
		if !time.Now().Before(deadline) {
			return "", fmt.Errorf("no agent in %s after %dms", pane, e.StartTimeoutMS)
		}
		time.Sleep(e.PromptPoll)
	}
}

// StartClaude starts Claude as Agent name in pane, in the workspace labelled
// label, then sends the Brief with PromptAgent. Behind a startup dialog
// (folder trust) a detached child sends the Brief once the user answers it.
// Returns ready, blocked or failed; why it failed goes to warn.
func (e *Env) StartClaude(name, pane, brief, label, model string, warn func(string)) string {
	if brief != "" {
		brief = core.Tag("[berthier brief]", brief)
	}
	return e.startClaude(name, pane, brief, label, model, warn)
}

// startClaude is StartClaude with a first prompt already tagged.
func (e *Env) startClaude(name, pane, brief, label, model string, warn func(string)) string {
	status, err := e.launch(pane, name, e.launchOpts(label, model))
	if err != nil {
		warn(err.Error())
		return "failed"
	}
	if status == "blocked" {
		if brief != "" {
			e.briefAfterDialogDetached(name, pane, brief)
		}
		return "blocked"
	}
	if brief != "" {
		if err := e.PromptAgent(pane, brief); err != nil {
			warn("Brief not delivered: " + err.Error())
			return "failed"
		}
	}
	return "ready"
}

// briefAfterDialogDetached re-runs this binary as `brief-after-dialog`,
// detached, so it outlives the command that started Claude.
func (e *Env) briefAfterDialogDetached(name, pane, brief string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "brief-after-dialog", name, pane, brief)
	cmd.Env = os.Environ()
	setDetached(cmd)
	if cmd.Start() == nil {
		cmd.Process.Release()
	}
}

// waitIdle waits until the Agent in pane is idle (BriefWait at most).
func (e *Env) waitIdle(pane string) bool {
	for deadline := time.Now().Add(e.BriefWait); time.Now().Before(deadline); time.Sleep(4 * e.PromptPoll) {
		if a, err := e.Host.Pane(pane); err == nil && a.Status == "idle" {
			return true
		}
	}
	return false
}

// BriefAfterDialog waits for the Agent to get past its startup dialog, then
// sends the Brief, logging the outcome.
func (e *Env) BriefAfterDialog(name, pane, brief string) {
	f := core.Fields{"pane": pane, "name": name}
	if !e.waitIdle(pane) {
		e.Event("brief_failed", fmt.Sprintf("Brief for %s (%s) not sent: Claude never got past its startup dialog", name, pane), with(f, "why", "startup dialog"))
	} else if err := e.PromptAgent(pane, brief); err == nil {
		e.Event("brief_sent", fmt.Sprintf("sent Brief to %s (%s) after its startup dialog", name, pane), f)
	} else {
		e.Event("brief_failed", fmt.Sprintf("Brief for %s (%s) not sent after its startup dialog: %s", name, pane, err), with(f, "why", err.Error()))
	}
}

func with(f core.Fields, kv ...any) core.Fields {
	g := core.Fields{}
	for k, v := range f {
		g[k] = v
	}
	for i := 0; i < len(kv); i += 2 {
		g[kv[i].(string)] = kv[i+1]
	}
	return g
}

// paneAtShell: is the pane sitting at a bare interactive shell, ready for a start?
func (e *Env) paneAtShell(pane string) bool {
	p, err := e.Host.Process(pane)
	return err == nil && p.AtShell
}

// selfPane is the pane this process runs in, "" outside the Host.
func (e *Env) selfPane() string {
	p, _ := e.Host.Self(e.getenv)
	return p
}

// selfWorkspace is the workspace this process runs in, "" outside the Host.
func (e *Env) selfWorkspace() string {
	_, w := e.Host.Self(e.getenv)
	return w
}
