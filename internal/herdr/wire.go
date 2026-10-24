package herdr

import (
	"errors"

	"berthier/internal/core"
)

// ErrUnreachable is Herdr not answering `api snapshot`.
var ErrUnreachable = errors.New("herdr unreachable")

type wireWorkspace struct {
	ID       string `json:"workspace_id"`
	Label    string `json:"label"`
	Number   int    `json:"number"`
	Worktree *struct {
		RepoRoot     *string `json:"repo_root"`
		CheckoutPath *string `json:"checkout_path"`
	} `json:"worktree"`
}

type wirePane struct {
	PaneID       string  `json:"pane_id"`
	WorkspaceID  string  `json:"workspace_id"`
	TabID        *string `json:"tab_id"`
	Agent        *string `json:"agent"`
	Status       string  `json:"agent_status"`
	Cwd          *string `json:"cwd"`
	Title        *string `json:"terminal_title_stripped"`
	Name         *string `json:"name"`
	StateSeq     *int64  `json:"state_change_seq"`
	AgentSession *struct {
		Value *string `json:"value"`
	} `json:"agent_session"`
}

func (w wireWorkspace) core() core.Workspace {
	out := core.Workspace{ID: w.ID, Label: w.Label, Number: w.Number}
	if w.Worktree != nil {
		out.Worktree = &core.Worktree{RepoRoot: w.Worktree.RepoRoot, CheckoutPath: w.Worktree.CheckoutPath}
	}
	return out
}

func (p wirePane) core() core.SnapAgent {
	out := core.SnapAgent{PaneID: p.PaneID, WorkspaceID: p.WorkspaceID, TabID: p.TabID, Agent: p.Agent,
		Status: p.Status, Cwd: p.Cwd, Title: p.Title, Name: p.Name, StateSeq: p.StateSeq}
	if p.AgentSession != nil {
		out.Session = p.AgentSession.Value
	}
	return out
}

func panes(in []wirePane) []core.SnapAgent {
	var out []core.SnapAgent
	for _, p := range in {
		out = append(out, p.core())
	}
	return out
}

// Workspaces is `herdr workspace list`.
func (c Client) Workspaces() ([]core.Workspace, error) {
	var r struct {
		Result struct{ Workspaces []wireWorkspace }
	}
	if err := c.JSON(&r, "workspace", "list"); err != nil {
		return nil, err
	}
	var out []core.Workspace
	for _, w := range r.Result.Workspaces {
		out = append(out, w.core())
	}
	return out, nil
}

// Snapshot is `herdr api snapshot`.
func (c Client) Snapshot() (core.Snapshot, error) {
	var r struct {
		Result struct {
			Snapshot *struct {
				Workspaces []wireWorkspace `json:"workspaces"`
				Agents     []wirePane      `json:"agents"`
				Panes      []wirePane      `json:"panes"`
			}
		}
	}
	if err := c.JSON(&r, "api", "snapshot"); err != nil || r.Result.Snapshot == nil {
		return core.Snapshot{}, ErrUnreachable
	}
	s := core.Snapshot{Agents: panes(r.Result.Snapshot.Agents), Panes: panes(r.Result.Snapshot.Panes)}
	for _, w := range r.Result.Snapshot.Workspaces {
		s.Workspaces = append(s.Workspaces, w.core())
	}
	return s, nil
}
