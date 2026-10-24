package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Herdr whose workspace `ws` is labelled `label`; any other command is a no-op.
func fakeWorkspace(t *testing.T, s *sandbox, ws, label string) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "herdr")
	os.WriteFile(bin, []byte(`#!/bin/sh
case "$1 $2 $3" in
  "workspace get "`+ws+`) echo '{"result":{"workspace":{"label":"`+label+`"}}}' ;;
esac
exit 0
`), 0o755)
	s.env["BERTHIER_HERDR"] = bin
}

func TestSessionStart(t *testing.T) {
	s := newSandbox(t)
	fakeSkipli(t, s)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "BERTHIER.md"), []byte("# You are Berthier\n\nPersona line.\n"), 0o644)
	s.env["BERTHIER_ROOT"] = root
	s.env["CLAUDE_CODE_SESSION_ID"] = "writer"
	e := LoadEnv(s.getenv)
	for _, c := range []string{"demo", "berthier"} {
		if _, err := e.EnsureBoard(c, ""); err != nil {
			t.Fatal(err)
		}
		s.sh("notes of "+c, "notes", c, "--set", "-")
	}
	start := func(cwd, sid string) string {
		out, _, code := s.sh(`{"session_id":"`+sid+`","cwd":"`+cwd+`"}`, "session-start")
		if code != 0 {
			t.Fatalf("session-start exited %d", code)
		}
		return out
	}

	// Berthier's repo root: the persona, then its Notes; the read is recorded for the session.
	fakeWorkspace(t, s, "w9", "berthier")
	got := start(root, "s1")
	eq(t, "root gets persona and Notes", got, "# You are Berthier\n\nPersona line.\n\n# berthier's Notes\n\nnotes of berthier\n")
	eq(t, "the read is recorded", strings.Contains(s.read("state/notes-read.json")+s.read("notes-read.json"), "s1"), true)

	// A Corps's Main Agent: its Corps's Notes only.
	s.env["HERDR_WORKSPACE_ID"] = "w1"
	fakeWorkspace(t, s, "w1", "demo")
	eq(t, "Corps gets its Notes", start(t.TempDir(), "s2"), "# demo's Notes\n\nnotes of demo\n")

	// A Division, an unknown workspace, no workspace: nothing.
	s.write("divisions.json", `{"dark-mode":{"corps":"demo","worktree":true}}`)
	fakeWorkspace(t, s, "w1", "dark-mode")
	eq(t, "Division prints nothing", start(t.TempDir(), "s3"), "")
	fakeWorkspace(t, s, "w1", "nobody")
	eq(t, "a Corps without Notes prints nothing", start(t.TempDir(), "s3"), "")
	delete(s.env, "HERDR_WORKSPACE_ID")
	eq(t, "outside the Army prints nothing", start(t.TempDir(), "s3"), "")

	// Slow Notes: the persona still goes out, promptly.
	slow := filepath.Join(t.TempDir(), "slow")
	os.WriteFile(slow, []byte("#!/bin/sh\nsleep 30\n"), 0o755)
	s.env["BERTHIER_SKIPLI_BIN"] = slow
	s.env["BERTHIER_SESSION_START_WAIT"] = "0.2"
	s.env["HERDR_WORKSPACE_ID"] = "w1"
	fakeWorkspace(t, s, "w1", "demo")
	eq(t, "slow Notes do not hold the session", start(root, "s4"), "# You are Berthier\n\nPersona line.\n\n")
}
