package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"berthier/internal/core"
)

// Notes: a Corps's open threads, commitments and recent decisions, kept with
// its Board so a new session of its Main Agent continues where the last one
// stopped. Only berthier touches the store (NotesStore, Army-wide).

// notesStore keeps every Corps's Notes.
type notesStore interface {
	get(corps string) (string, error)
	set(corps, text string) error
}

func (e *Env) notesStore() (notesStore, error) {
	switch e.NotesStore {
	case "skipli":
		return skipliNotes{e}, nil
	}
	return nil, fmt.Errorf("BERTHIER_NOTES_STORE=%s: the only store is skipli", e.NotesStore)
}

// skipliNotes keeps Notes on each Corps's Board: the description of its card
// on the Grand Army Board, or of the "notes" card at the top of its own Board.
type skipliNotes struct{ e *Env }

// where finds a Corps's Board, and the card holding its Notes ("" when its
// own Board has none yet).
func (s skipliNotes) where(corps string) (core.BoardDoc, string, error) {
	rec, ok := s.e.loadBoards().Corps[corps]
	if !ok {
		return core.BoardDoc{}, "", fmt.Errorf("%s has no Board yet: berthier board %s", corps, corps)
	}
	doc, err := s.e.skipliBoard(rec.Board)
	if err != nil {
		return doc, "", err
	}
	if rec.Card != "" {
		if _, ok := cardIn(doc, rec.Card); ok {
			return doc, rec.Card, nil
		}
		if id, ok := core.BoardCardNamed(doc, corps); ok {
			return doc, id, nil
		}
		return doc, "", fmt.Errorf("%s's card %s is gone from its Board %s", corps, rec.Card, rec.Board)
	}
	id, _ := core.BoardCardNamed(doc, core.NotesCard)
	return doc, id, nil
}

func (s skipliNotes) get(corps string) (string, error) {
	doc, card, err := s.where(corps)
	if err != nil || card == "" {
		return "", err
	}
	c, _ := cardIn(doc, card)
	return c.Description, nil
}

func (s skipliNotes) set(corps, text string) error {
	doc, card, err := s.where(corps)
	if err != nil {
		return err
	}
	if card == "" {
		// Its own Board's first Notes: a "notes" card, at the top.
		if _, err := s.e.skipli("add-card", doc.ID, core.NotesCard); err != nil {
			return err
		}
		if doc, err = s.e.skipliBoard(doc.ID); err != nil {
			return err
		}
		var ok bool
		if card, ok = core.BoardCardNamed(doc, core.NotesCard); !ok {
			return fmt.Errorf("card %q not found on Board %s after adding it", core.NotesCard, doc.ID)
		}
		if first := doc.Cards[0].ID; first != card {
			if _, err := s.e.skipli("move-card", doc.ID, card, "--before", first); err != nil {
				return err
			}
		}
	}
	_, err = s.e.skipli("edit-card", doc.ID, card, "--description", text)
	return err
}

// notesReader is who read the Notes: the Agent's Claude session (a Fresh
// Start is a new reader), else its pane, else the user's terminal.
func (e *Env) notesReader() string {
	if sid := e.getenv("CLAUDE_CODE_SESSION_ID"); sid != "" {
		return sid
	}
	if pane := e.getenv("HERDR_PANE_ID"); pane != "" {
		return "pane:" + pane
	}
	return "terminal"
}

func notesHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// noteRead remembers which version of a Corps's Notes the reader last saw.
func (e *Env) noteRead(corps, text string) error { return e.noteReadBy(e.notesReader(), corps, text) }

// noteReadBy is noteRead for an explicit reader (a session-start hook knows its session only from its input).
func (e *Env) noteReadBy(reader, corps, text string) error {
	m := map[string]map[string]string{}
	readJSON(e.NotesRead, &m)
	if m[corps] == nil {
		m[corps] = map[string]string{}
	}
	m[corps][reader] = notesHash(text)
	return writeJSONAtomic(e.NotesRead, m, true)
}

func (e *Env) lastRead(corps string) (string, bool) {
	m := map[string]map[string]string{}
	readJSON(e.NotesRead, &m)
	h, ok := m[corps][e.notesReader()]
	return h, ok
}

// callerCorps is the Corps of the Agent running a command: its own, or for a
// Division the Corps it belongs to (Berthier's is its workspace's).
func (e *Env) callerCorps() (string, error) {
	pane := e.getenv("HERDR_PANE_ID")
	if pane == "" {
		return "", die("name the Corps (not run from an Agent's Herdr pane)")
	}
	st := e.ReadState()
	a := st.Agents.Get(pane)
	if a == nil || a.WorkspaceLabel == nil {
		return "", die("pane %s is not a known Agent yet; name the Corps", pane)
	}
	return e.ReadDivisions().Owner(*a.WorkspaceLabel), nil
}

// cmdNotes prints a Corps's Notes, or with --set - replaces them from stdin,
// refused if they changed since this Agent last read them.
func cmdNotes(e *Env, args []string, stdio IO) error {
	const usage = "usage: berthier notes [<corps>] [--set -]"
	set := false
	var pos []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--set" && i+1 < len(args) && args[i+1] == "-":
			set = true
			i++
		case strings.HasPrefix(args[i], "-"):
			return die("%s", usage)
		default:
			pos = append(pos, args[i])
		}
	}
	if len(pos) > 1 {
		return die("%s", usage)
	}
	var corps string
	if len(pos) == 1 {
		corps = core.CorpsName(pos[0])
	} else {
		c, err := e.callerCorps()
		if err != nil {
			return err
		}
		corps = c
	}
	store, err := e.notesStore()
	if err != nil {
		return err
	}
	// stdin is read before the lock: a writer whose pipe never closes must not block every reader
	var b []byte
	if set {
		if b, err = io.ReadAll(stdio.In); err != nil {
			return err
		}
	}
	unlock, err := lockFile(filepath.Join(e.Home, "notes.lock"), true)
	if err != nil {
		return err
	}
	defer unlock()
	now, err := store.get(corps)
	if err != nil {
		return err
	}
	if !set {
		if err := e.noteRead(corps, now); err != nil {
			return err
		}
		if now == "" {
			fmt.Fprintf(stdio.Err, "(%s has no Notes yet; write them with: berthier notes %s --set - <<'EOF')\n", corps, corps)
			return nil
		}
		fmt.Fprintln(stdio.Out, strings.TrimRight(now, "\n"))
		return nil
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return die("refusing to empty %s's Notes (stdin is empty)", corps)
	}
	if len(text) > e.NotesMax {
		return die("Notes of %d bytes are over the %d-byte cap: prune resolved threads and old decisions", len(text), e.NotesMax)
	}
	if h, ok := e.lastRead(corps); now != "" && (!ok || h != notesHash(now)) {
		return die("%s's Notes changed since you last read them: run berthier notes %s, merge your change in, and set again", corps, corps)
	}
	if text == strings.TrimSpace(now) {
		fmt.Fprintf(stdio.Out, "%s's Notes unchanged\n", corps)
		return e.noteRead(corps, now)
	}
	if err := store.set(corps, text); err != nil {
		return err
	}
	if err := e.noteRead(corps, text); err != nil {
		return err
	}
	fmt.Fprintf(stdio.Out, "%s's Notes saved (%d bytes)\n", corps, len(text))
	return nil
}
