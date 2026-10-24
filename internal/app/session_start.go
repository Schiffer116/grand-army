package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"berthier/internal/core"
)

// cmdSessionStart is a Claude Code SessionStart hook (JSON on stdin). Berthier's
// repo root gets BERTHIER.md and the Berthier Notes; any other Agent in a Corps's
// workspace its Corps's Notes; a Division or anything outside the Army nothing.
// Never fails the session: it prints what it has within BERTHIER_SESSION_START_WAIT.
func cmdSessionStart(e *Env, args []string, io IO) error {
	var in struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
	}
	json.NewDecoder(io.In).Decode(&in)
	persona := sameDir(in.Cwd, e.Root)
	corps := "berthier"
	if !persona {
		corps = e.sessionCorps()
	}
	if corps == "" {
		return nil
	}
	var b strings.Builder
	if persona {
		if raw, err := os.ReadFile(filepath.Join(e.Root, "BERTHIER.md")); err == nil {
			b.WriteString(strings.TrimRight(string(raw), "\n") + "\n\n")
		}
	}
	// The Notes read is a skipli call: bounded, and the persona goes out regardless.
	got := make(chan string, 1)
	go func() { got <- e.notesForSession(corps, in.SessionID) }()
	select {
	case notes := <-got:
		if notes != "" {
			fmt.Fprintf(&b, "# %s's Notes\n\n%s\n", corps, notes)
		}
	case <-time.After(e.SessionWait):
	}
	fmt.Fprint(io.Out, b.String())
	return nil
}

// sessionCorps is the Corps whose Main Agent runs in this pane's workspace, or ""
// for a Division or no workspace of the Army.
func (e *Env) sessionCorps() string {
	id := e.getenv("HERDR_WORKSPACE_ID")
	if id == "" {
		return ""
	}
	var r struct {
		Result struct{ Workspace struct{ Label string } }
	}
	if e.H.JSON(&r, "workspace", "get", id) != nil || r.Result.Workspace.Label == "" {
		return ""
	}
	if e.ReadDivisions().IsDivision(r.Result.Workspace.Label) {
		return ""
	}
	return core.CorpsName(r.Result.Workspace.Label)
}

// notesForSession reads a Corps's Notes for the session, as `berthier notes`
// does (so its later --set is not refused). Empty when it has none or no Board.
func (e *Env) notesForSession(corps, sid string) string {
	store, err := e.notesStore()
	if err != nil {
		return ""
	}
	text, err := store.get(corps)
	if err != nil || text == "" {
		return ""
	}
	if sid != "" {
		unlock, err := lockFile(filepath.Join(e.Home, "notes.lock"), true)
		if err == nil {
			defer unlock()
			e.noteReadBy(sid, corps, text)
		}
	}
	return strings.TrimRight(text, "\n")
}

// sameDir: a and b are the same directory (symlinks resolved).
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}
