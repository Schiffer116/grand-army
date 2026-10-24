package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"berthier/internal/core"
)

// cmdSessionStart is a Claude Code SessionStart hook (JSON on stdin). It prints
// the session's role: Berthier (the repo root) gets BERTHIER.md and the
// Berthier Notes; a Corps's Marshal MARSHAL.md and its Corps's Notes; a General
// (the Agent of a Division) GENERAL.md; any other session, outside the Army,
// nothing. Never fails the session: it prints what it has within
// BERTHIER_SESSION_START_WAIT.
func cmdSessionStart(e *Env, args []string, io IO) error {
	payload, _ := stdioReadAll(io.In)
	in, _ := e.harness().Hook("SessionStart", payload)
	role, corps := "", "berthier"
	if sameDir(in.Cwd, e.Root) {
		role = "berthier"
	} else {
		role, corps = e.sessionRole()
	}
	if role == "" {
		return nil
	}
	var b strings.Builder
	if raw, err := os.ReadFile(filepath.Join(e.Root, strings.ToUpper(role)+".md")); err == nil {
		b.WriteString(strings.TrimRight(string(raw), "\n") + "\n\n")
	}
	if role == "general" {
		fmt.Fprint(io.Out, b.String())
		return nil
	}
	// The Notes read is a skipli call: bounded, and the instructions go out regardless.
	got := make(chan string, 1)
	go func() { got <- e.notesForSession(corps, in.Session) }()
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

// sessionRole is the role of the Agent running in this pane's workspace:
// "marshal" of a Corps, "general" of a Division, or "" outside the Army.
func (e *Env) sessionRole() (role, corps string) {
	id := e.selfWorkspace()
	if id == "" {
		return "", ""
	}
	ws, err := e.Host.Workspace(id)
	if err != nil || ws.Label == "" {
		return "", ""
	}
	if e.ReadDivisions().IsDivision(ws.Label) {
		return "general", ""
	}
	return "marshal", core.CorpsName(ws.Label)
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
