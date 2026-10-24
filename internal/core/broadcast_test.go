package core

import (
	"slices"
	"testing"
)

func TestBroadcastText(t *testing.T) {
	for _, tc := range []struct {
		name, kind, extra string
		max               int
		want              string
		ok                bool
	}{
		{"shutdown", "shutdown", "", 100, TagShutdown + " " + broadcasts["shutdown"].text, true},
		{"shutdown extra", "shutdown", "  back at 9 ", 100, TagShutdown + " " + broadcasts["shutdown"].text + " back at 9", true},
		{"capped", "shutdown", "abcdefgh", 5, TagShutdown + " " + broadcasts["shutdown"].text + " abcde [truncated]", true},
		{"wake", "wake", "", 100, TagWake + " " + broadcasts["wake"].text, true},
		{"unknown", "reboot", "", 100, "", false},
	} {
		got, ok := BroadcastText(tc.kind, tc.extra, tc.max)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: %q %v, want %q %v", tc.name, got, ok, tc.want, tc.ok)
		}
		if ok && OriginOf(got) != tc.kind {
			t.Errorf("%s: origin %q", tc.name, OriginOf(got))
		}
	}
}

func TestBroadcastTargets(t *testing.T) {
	lbl := func(s string) *string { return &s }
	frozen := Frozen{}
	ag := Agents{Keys: []string{"b", "m", "d", "f", "g", "n"}, M: map[string]*Agent{
		"b": {Status: "idle", WorkspaceLabel: lbl("berthier")},
		"m": {Status: "working", WorkspaceLabel: lbl("Skipli")},
		"d": {Status: "blocked", WorkspaceLabel: lbl("shutdown-wake")},
		"f": {Status: "gone", WorkspaceLabel: lbl("old"), Frozen: &frozen},
		"g": {Status: "gone", WorkspaceLabel: lbl("dead")},
		"n": {Status: "idle"},
	}}
	to, skipped := BroadcastTargets(ag, lbl("b"))
	check(t, "targets", []any{to, skipped}, `[["berthier","skipli","shutdown-wake"],1]`)
}

func TestRebuild(t *testing.T) {
	check(t, "rebuild", []string{Rebuild("", ""), Rebuild("halfway", ""), Rebuild("halfway", "Build it")},
		`["","\nYour last report: halfway","\nYour last report: halfway\nYour branch holds your progress so far. Your original Brief:\n\nBuild it"]`)
	check(t, "fresh prompt carries it", FreshPrompt("halfway", "Build it") == "[berthier fresh] You were restarted fresh: run berthier notes, read your Board, your last report and git status, then continue."+Rebuild("halfway", "Build it"), `true`)
}

func TestWorkingNames(t *testing.T) {
	ag := Agents{}
	for _, a := range []struct{ pane, label, status string }{
		{"p1", "Busy", "working"}, {"p2", "calm", "idle"}, {"p3", "stuck", "blocked"}, {"p4", "fin", "done"}, {"p5", "dead", "gone"}, {"b", "grand-army", "working"},
	} {
		ag.Set(a.pane, &Agent{Status: a.status, WorkspaceLabel: ptr(a.label)})
	}
	ag.M["p3"].Frozen = &Frozen{}
	ag.Set("p6", &Agent{Status: "blocked", WorkspaceLabel: ptr("ask")})
	got := WorkingNames(ag, ptr("b"))
	if want := []string{"busy", "berthier", "ask"}; !slices.Equal(got, want) {
		t.Errorf("WorkingNames = %v, want %v", got, want)
	}
}

func TestWakeSplit(t *testing.T) {
	awake, idle, absent := WakeSplit([]string{"a", "b", "c"}, []string{"c", "z", "a", "z"})
	if !slices.Equal(awake, []string{"a", "c"}) || !slices.Equal(idle, []string{"b"}) || !slices.Equal(absent, []string{"z"}) {
		t.Errorf("WakeSplit = %v %v %v", awake, idle, absent)
	}
}

func TestWithoutRetired(t *testing.T) {
	s := Snapshot{
		Workspaces: []Workspace{{ID: "w1", Label: "Keep"}, {ID: "w2", Label: ".Old"}},
		Agents:     []SnapAgent{{PaneID: "a", WorkspaceID: "w1"}, {PaneID: "b", WorkspaceID: "w2"}},
		Panes:      []SnapAgent{{PaneID: "a", WorkspaceID: "w1"}, {PaneID: "b", WorkspaceID: "w2"}},
	}
	got := WithoutRetired(s, []string{"old"})
	if len(got.Workspaces) != 1 || got.Workspaces[0].ID != "w1" || len(got.Agents) != 1 || len(got.Panes) != 1 || got.Panes[0].PaneID != "a" {
		t.Errorf("WithoutRetired = %+v", got)
	}
	if len(WithoutRetired(s, nil).Panes) != 2 {
		t.Error("nothing retired changed the snapshot")
	}
}
