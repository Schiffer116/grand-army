package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNotes(t *testing.T) {
	s := newSandbox(t)
	sk := fakeSkipli(t, s)
	e := LoadEnv(s.getenv)
	if _, err := e.EnsureBoard("demo", ""); err != nil {
		t.Fatal(err)
	}
	as := func(session string) { s.env["CLAUDE_CODE_SESSION_ID"] = session }
	card := e.loadBoards().Corps["demo"].Card

	as("s1")
	out, errs, code := s.sh("", "notes", "demo")
	eq(t, "no Notes yet", []any{out, strings.Contains(errs, "no Notes yet"), code}, "[ true 0]")
	_, errs, code = s.sh("", "notes", "nobody")
	eq(t, "a Corps without a Board", []any{strings.Contains(errs, "berthier board nobody"), code}, "[true 1]")
	out, _, code = s.sh("open: \"quotes\"\nline two\n", "notes", "demo", "--set", "-")
	eq(t, "first Notes", []any{out, code, readFile(filepath.Join(sk, "desc."+card))}, "[demo's Notes saved (23 bytes)\n 0 open: \"quotes\"\nline two]")
	out, _, _ = s.sh("", "notes", "demo")
	eq(t, "read back", out, "open: \"quotes\"\nline two\n")

	as("s2")
	_, errs, code = s.sh("mine", "notes", "demo", "--set", "-")
	eq(t, "never read: refused", []any{strings.Contains(errs, "changed since you last read them"), code}, "[true 1]")
	s.sh("", "notes", "demo")
	_, _, code = s.sh("s2's", "notes", "demo", "--set", "-")
	eq(t, "read, then set", code, 0)
	as("s1")
	_, errs, code = s.sh("s1's", "notes", "demo", "--set", "-")
	eq(t, "changed meanwhile: refused", []any{strings.Contains(errs, "merge"), code, readFile(filepath.Join(sk, "desc."+card))}, "[true 1 s2's]")
	_, errs, code = s.sh("", "notes", "demo", "--set", "-")
	eq(t, "never emptied", []any{strings.Contains(errs, "empty"), code}, "[true 1]")
	s.env["BERTHIER_NOTES_MAX"] = "10"
	s.sh("", "notes", "demo")
	_, errs, code = s.sh("more than ten bytes", "notes", "demo", "--set", "-")
	eq(t, "over the cap", []any{strings.Contains(errs, "cap"), code}, "[true 1]")
	delete(s.env, "BERTHIER_NOTES_MAX")

	// A Corps with its own Board: a "notes" card at its top.
	appendLine(t, filepath.Join(sk, "boards"), "own1|Skipli")
	appendLine(t, filepath.Join(sk, "cards"), "own1|c-ui|UI")
	if _, err := e.EnsureBoard("skipli", "own1"); err != nil {
		t.Fatal(err)
	}
	_, _, code = s.sh("own notes", "notes", "skipli", "--set", "-")
	first := strings.SplitN(readFile(filepath.Join(sk, "cards")), "\nown1|", 2)[1]
	first = strings.SplitN(first, "\n", 2)[0]
	notes := strings.Split(first, "|")[0]
	eq(t, "notes card on top", []any{code, strings.HasSuffix(first, "|notes"), readFile(filepath.Join(sk, "desc."+notes))}, "[0 true own notes]")
	s.sh("", "notes", "skipli")
	s.sh("own notes 2", "notes", "skipli", "--set", "-")
	eq(t, "one notes card", strings.Count(readFile(filepath.Join(sk, "cards")), "|notes"), 1)

	// The caller's Corps by default; a Division's is its Corps's.
	s.write("state.json", `{"agents":{"w2:p1":{"status":"idle","workspace_id":"w2","workspace_label":"dark-mode"}}}`)
	s.write("divisions.json", `{"dark-mode":{"corps":"demo","worktree":true}}`)
	s.env["HERDR_PANE_ID"] = "w2:p1"
	out, _, _ = s.sh("", "notes")
	eq(t, "a Division reads its Corps's Notes", out, "s2's\n")
	os.Remove(filepath.Join(sk, "desc."+card))
}
