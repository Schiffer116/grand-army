package core

import "slices"

// WithoutRetired is the snapshot minus the workspaces of the retired Corps
// (CorpsName'd) and their panes and agents: the Main Loop never sees them, so
// it does not Revive, escalate to, deliver to or show them.
func WithoutRetired(s Snapshot, retired []string) Snapshot {
	if len(retired) == 0 {
		return s
	}
	gone := map[string]bool{}
	var out Snapshot
	for _, w := range s.Workspaces {
		if slices.Contains(retired, CorpsName(w.Label)) {
			gone[w.ID] = true
		} else {
			out.Workspaces = append(out.Workspaces, w)
		}
	}
	keep := func(l []SnapAgent) (o []SnapAgent) {
		for _, a := range l {
			if !gone[a.WorkspaceID] {
				o = append(o, a)
			}
		}
		return o
	}
	out.Agents, out.Panes = keep(s.Agents), keep(s.Panes)
	return out
}
