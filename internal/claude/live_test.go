package claude_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"berthier/internal/claude"
	"berthier/internal/harness"
	"berthier/internal/herdr"
	"berthier/internal/host"
)

// The Harness against the real Claude Code, running in a pane of the named
// test Herdr session BERTHIER_CONTRACT_HERDR_SESSION (tests/e2e.sh sets it):
// its launch, its screens as they really look, its keys. Nothing is sent to
// the model.
func TestLiveClaude(t *testing.T) {
	session := os.Getenv("BERTHIER_CONTRACT_HERDR_SESSION")
	if session == "" {
		t.Skip("BERTHIER_CONTRACT_HERDR_SESSION is not set (tests/e2e.sh runs this)")
	}
	var h host.Host = herdr.Herdr{Bin: "herdr", Session: session}
	var c harness.Harness = claude.Claude{ConfigDir: os.Getenv("CLAUDE_CONFIG_DIR")}
	cwd, _ := os.Getwd()
	made, err := h.CreateWorkspace(cwd, "live-claude")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.CloseWorkspace(made.WorkspaceID) })
	pane := made.PaneID
	wait := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			if cond() {
				return
			}
		}
		screen, _ := h.Read(pane, host.ReadOpts{})
		t.Fatalf("timed out waiting for %s; the pane shows:\n%s", what, screen)
	}
	wait("a shell", func() bool { p, _ := h.Process(pane); return p.AtShell })
	// The hooks drop their payloads in files: what Claude really sends.
	hooks := t.TempDir()
	reg := map[string]any{}
	for _, ev := range []string{"SessionStart", "SessionEnd"} {
		reg[ev] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "cat > " + filepath.Join(hooks, ev+".json")}}}}
	}
	settings, _ := json.Marshal(map[string]any{"hooks": reg})
	if err := h.Start(pane, c.Launch(harness.LaunchOpts{Model: "haiku", Settings: string(settings)})); err != nil {
		t.Fatal(err)
	}
	payload := func(ev string) []byte {
		t.Helper()
		var b []byte
		wait(ev+" hook", func() bool { b, _ = os.ReadFile(filepath.Join(hooks, ev+".json")); return len(b) > 0 })
		return b
	}
	start, err := c.Hook("SessionStart", payload("SessionStart"))
	if err != nil || start.State != harness.Started || start.Session == "" {
		t.Errorf("SessionStart hook = %+v, %v", start, err)
	}
	wait("Claude's box", func() bool {
		screen, _ := h.Read(pane, host.ReadOpts{})
		return strings.Contains(screen, "❯")
	})

	draft := func() bool {
		screen, err := h.Read(pane, host.ReadOpts{ANSI: true})
		if err != nil {
			t.Fatal(err)
		}
		return c.Draft(screen)
	}
	if draft() {
		t.Fatal("a fresh Claude's suggestion is a draft")
	}
	const typed = "half a thought\nand a second line"
	if err := h.Type(pane, typed); err != nil {
		t.Fatal(err)
	}
	wait("the draft", draft)
	if screen, _ := h.Read(pane, host.ReadOpts{}); !c.PromptHeld(screen, typed) {
		t.Errorf("PromptHeld does not see the text in the box:\n%s", screen)
	}
	// One press clears one line of a draft; Berthier presses until it is gone.
	for i := 0; i < 20 && draft(); i++ {
		for _, k := range c.Keys(harness.ClearDraft) {
			h.Key(pane, k)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if draft() {
		t.Error("ClearDraft left a draft")
	}

	// Escape interrupts a turn, not the session: with no turn, Claude stays.
	for _, k := range c.Keys(harness.Interrupt) {
		h.Key(pane, k)
	}
	time.Sleep(time.Second)
	if p, _ := h.Process(pane); !p.Alive() || !strings.Contains(p.Name, "claude") {
		t.Errorf("Interrupt ended the session: %+v", p)
	}
	// End quits it, back to the shell.
	for _, k := range c.Keys(harness.End) {
		h.Key(pane, k)
		time.Sleep(300 * time.Millisecond)
	}
	wait("back at the shell", func() bool { p, _ := h.Process(pane); return p.AtShell })
	end, err := c.Hook("SessionEnd", payload("SessionEnd"))
	if err != nil || end.State != harness.Exited || end.Session != start.Session || !c.DeliberateExit(end.Reason) {
		t.Errorf("SessionEnd hook after End = %+v, %v; want an exit of %s the user chose", end, err, start.Session)
	}
}
