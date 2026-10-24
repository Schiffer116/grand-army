package app

import (
	"fmt"
	"strconv"
	"time"

	"berthier/internal/core"
	"berthier/internal/harness"
	"berthier/internal/host"
)

// The commands an Agent uses to look at and steer another one: the Host is
// never named to Agents (ADR 0006), these are the whole surface.

// cmdCheckIn prints what a pane shows: its visible screen, or its last N
// lines. For a dialog or a blocked question; the Agent's last message is
// `berthier last`.
func cmdCheckIn(e *Env, args []string, io IO) error {
	usage := die("usage: berthier check-in <pane> [--recent N]")
	o := host.ReadOpts{}
	switch {
	case len(args) == 1:
	case len(args) == 3 && args[1] == "--recent":
		n, err := strconv.Atoi(args[2])
		if err != nil || n < 1 {
			return usage
		}
		o.Recent = n
	default:
		return usage
	}
	out, err := e.Host.Read(args[0], o)
	if err != nil {
		return die("check-in %s: %v", args[0], err)
	}
	fmt.Fprint(io.Out, out)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		fmt.Fprintln(io.Out)
	}
	return nil
}

// AgentInfo is `berthier army <pane>`: one Agent.
type AgentInfo struct {
	Pane     string  `json:"pane"`
	Name     *string `json:"name"`
	State    string  `json:"state"` // idle, working, blocked, gone, or unknown for a pane no hook speaks for
	Corps    string  `json:"corps"`
	Division string  `json:"division,omitempty"`
	Cwd      *string `json:"cwd"`
	Process  string  `json:"process"` // alive, at shell, or none
	Session  *string `json:"session"`
}

// AgentInfoOf describes the pane: the Army's view of its Agent when it has
// one, else what the Host knows of the pane.
func (e *Env) AgentInfoOf(pane string) (AgentInfo, error) {
	p, err := e.Host.Pane(pane)
	if err != nil {
		return AgentInfo{}, die("army %s: %v", pane, err)
	}
	info := AgentInfo{Pane: pane, Name: p.Name, State: "unknown", Cwd: p.Cwd, Process: "none"}
	if pr, err := e.Host.Process(pane); err == nil {
		switch {
		case pr.AtShell:
			info.Process = "at shell"
		case pr.Alive():
			info.Process = "alive"
		}
	}
	v := e.ArmyView()
	look := func(corps, division string, agents []core.AgentView) bool {
		for _, a := range agents {
			if a.PaneID != pane {
				continue
			}
			info.Corps, info.Division = corps, division
			info.Name, info.Cwd, info.Session = a.Name, cmpPtr(a.Cwd, info.Cwd), a.SessionID
			if a.Status != nil {
				info.State = *a.Status
			}
			return true
		}
		return false
	}
	if v.Berthier != nil && look("berthier", "", []core.AgentView{*v.Berthier}) {
		return info, nil
	}
	for _, d := range v.BerthierDivisions {
		if look("berthier", d.Name, d.Agents) {
			return info, nil
		}
	}
	for _, c := range v.Corps {
		if look(c.Name, "", c.Agents) {
			return info, nil
		}
		for _, d := range c.Divisions {
			if look(c.Name, d.Name, d.Agents) {
				return info, nil
			}
		}
	}
	// Not an Agent: say which Corps its workspace is.
	if ws, err := e.Host.Workspace(p.WorkspaceID); err == nil {
		info.Corps = core.CorpsName(ws.Label)
		if e.ReadDivisions().IsDivision(ws.Label) {
			info.Division = ws.Label
		}
	}
	return info, nil
}

func cmpPtr(a, b *string) *string {
	if a != nil {
		return a
	}
	return b
}

// cmdKeys presses a named key in a pane: interrupt (stop its turn), enter
// (submit what its box holds), clear-draft (empty its box).
func cmdKeys(e *Env, args []string, io IO) error {
	intents := map[string]harness.Intent{"interrupt": harness.Interrupt, "enter": harness.Submit, "clear-draft": harness.ClearDraft}
	var in harness.Intent
	if len(args) == 2 {
		in = intents[args[1]]
	}
	if in == "" {
		return die("usage: berthier keys <pane> interrupt|enter|clear-draft")
	}
	pane := args[0]
	press := func() error {
		for _, k := range e.harness().Keys(in) {
			if err := e.Host.Key(pane, k); err != nil {
				return die("keys %s: %v", pane, err)
			}
		}
		return nil
	}
	if in != harness.ClearDraft {
		return press()
	}
	// One press clears one line of a draft: press until the box is empty.
	for range 20 {
		screen, err := e.Host.Read(pane, host.ReadOpts{ANSI: true})
		if err != nil {
			return die("keys %s: %v", pane, err)
		}
		if !e.harness().Draft(screen) {
			return nil
		}
		if err := press(); err != nil {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return die("keys %s: its box still holds a draft", pane)
}

// cmdRename names the pane's Agent.
func cmdRename(e *Env, args []string, io IO) error {
	if len(args) != 2 || args[1] == "" {
		return die("usage: berthier rename <pane> <name>")
	}
	if err := e.Host.RenamePane(args[0], args[1]); err != nil {
		return die("rename %s: %v", args[0], err)
	}
	return nil
}
