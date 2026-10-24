package herdr

import "berthier/internal/core"

// Herdr's wire shapes, mapped to core's.

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
	PaneID      string  `json:"pane_id"`
	WorkspaceID string  `json:"workspace_id"`
	TabID       *string `json:"tab_id"`
	Cwd         *string `json:"cwd"`
	Title       *string `json:"terminal_title_stripped"`
	Name        *string `json:"name"`
	Label       *string `json:"label"`
}

func (w wireWorkspace) core() core.Workspace {
	out := core.Workspace{ID: w.ID, Label: w.Label, Number: w.Number}
	if w.Worktree != nil {
		out.Worktree = &core.Worktree{RepoRoot: w.Worktree.RepoRoot, CheckoutPath: w.Worktree.CheckoutPath}
	}
	return out
}

// core maps a pane; withLabel names it by its label when it has no agent name.
func (p wirePane) core(withLabel bool) core.SnapAgent {
	out := core.SnapAgent{PaneID: p.PaneID, WorkspaceID: p.WorkspaceID, TabID: p.TabID,
		Cwd: p.Cwd, Title: p.Title, Name: p.Name}
	if out.Name == nil && withLabel {
		out.Name = p.Label
	}
	return out
}

func panes(in []wirePane, withLabel bool) []core.SnapAgent {
	var out []core.SnapAgent
	for _, p := range in {
		out = append(out, p.core(withLabel))
	}
	return out
}
