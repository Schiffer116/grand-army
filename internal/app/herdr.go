package app

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"berthier/internal/core"
)

// Workspaces lists Herdr's workspaces.
func (e *Env) Workspaces() ([]core.Workspace, error) {
	var r struct {
		Result struct{ Workspaces []core.Workspace }
	}
	err := e.H.JSON(&r, "workspace", "list")
	return r.Result.Workspaces, err
}

// Snapshot is `herdr api snapshot`.
func (e *Env) Snapshot() (core.Snapshot, error) {
	var r struct {
		Result struct{ Snapshot *core.Snapshot }
	}
	if err := e.H.JSON(&r, "api", "snapshot"); err != nil || r.Result.Snapshot == nil {
		return core.Snapshot{}, fmt.Errorf("herdr unreachable")
	}
	return *r.Result.Snapshot, nil
}

// claudeFlags are the flags Claude starts with after `--` in the workspace
// labelled label: its model, and what its role allows. A Division's Agent may
// not use Artifact unless division-new --artifact. A Main Agent (not a
// Division) with no recorded model runs on CorpsModel. Berthier's persona comes
// from the SessionStart hook (`berthier session-start`), not from a flag.
func (e *Env) claudeFlags(label, model string) []string {
	var f []string
	_, isDivision := e.ReadDivisions()[core.CorpsName(label)]
	if model == "" && !isDivision {
		model = e.CorpsModel
	}
	if model != "" {
		f = append(f, "--model", model)
	}
	name := core.CorpsName(label)
	if d, ok := e.ReadDivisions()[name]; ok && !d.Artifact {
		f = append(f, "--disallowedTools", "Artifact")
	}
	return f
}

// StartClaude starts Claude as Agent name in pane, in the workspace labelled
// label, then sends the Brief with PromptAgent: Herdr refuses multi-line start
// arguments. Behind a startup dialog (folder trust) a detached child sends the
// Brief once the user answers it. Returns ready, blocked or failed; why it
// failed goes to warn.
func (e *Env) StartClaude(name, pane, brief, label, model string, warn func(string)) string {
	if brief != "" {
		brief = core.Tag("[berthier brief]", brief)
	}
	return e.startClaude(name, pane, brief, label, model, warn)
}

// startClaude is StartClaude with a first prompt already tagged.
func (e *Env) startClaude(name, pane, brief, label, model string, warn func(string)) string {
	args := []string{"agent", "start", name, "--kind", "claude", "--pane", pane, "--timeout", fmt.Sprint(e.StartTimeoutMS)}
	if flags := e.claudeFlags(label, model); len(flags) > 0 {
		args = append(append(args, "--"), flags...)
	}
	status := "started"
	_, startErr := e.H.Run(args...)
	if startErr != nil {
		var got struct {
			Result struct {
				Agent struct {
					Status string `json:"agent_status"`
				}
			}
		}
		e.H.JSON(&got, "agent", "get", pane)
		status = got.Result.Agent.Status
	}
	switch status {
	case "blocked":
		if brief != "" {
			e.briefAfterDialogDetached(name, pane, brief)
		}
		return "blocked"
	case "started", "idle", "done", "working", "unknown":
		// A timed-out start leaves the Agent unnamed.
		if status != "started" {
			if _, err := e.H.Run("agent", "rename", pane, name); err != nil {
				return "failed"
			}
		}
		if brief != "" {
			if err := e.H.PromptAgent(pane, brief); err != nil {
				warn("Brief not delivered: " + err.Error())
				return "failed"
			}
		}
		return "ready"
	}
	if startErr != nil {
		warn(startErr.Error())
	}
	return "failed"
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

// BriefAfterDialog waits for the Agent to get past its startup dialog, then
// sends the Brief, logging the outcome.
func (e *Env) BriefAfterDialog(name, pane, brief string) {
	f := core.Fields{"pane": pane, "name": name}
	if _, err := e.H.Run("agent", "wait", pane, "--until", "idle", "--timeout", "3600000"); err != nil {
		e.Event("brief_failed", fmt.Sprintf("Brief for %s (%s) not sent: Claude never got past its startup dialog", name, pane), with(f, "why", "startup dialog"))
	} else if err := e.H.PromptAgent(pane, brief); err == nil {
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

// paneAtShell: is the pane sitting at a bare interactive shell, ready for `agent start`?
func (e *Env) paneAtShell(pane string) bool {
	var r struct {
		Result struct {
			ProcessInfo struct {
				Foreground []struct{ Name string } `json:"foreground_processes"`
			} `json:"process_info"`
		}
	}
	if e.H.JSON(&r, "pane", "process-info", "--pane", pane) != nil || len(r.Result.ProcessInfo.Foreground) == 0 {
		return false
	}
	switch strings.TrimPrefix(r.Result.ProcessInfo.Foreground[0].Name, "-") {
	case "zsh", "bash", "fish", "sh", "dash", "nu":
		return true
	}
	return false
}
