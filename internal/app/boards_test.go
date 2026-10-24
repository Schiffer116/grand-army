package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"berthier/internal/core"
)

// fakeSkipli installs tests/fake-skipli; the test edits its files as the user.
func fakeSkipli(t *testing.T, s *sandbox) (dir string) {
	src, err := os.ReadFile("../../tests/fake-skipli")
	if err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	os.WriteFile(filepath.Join(dir, "skipli-cli"), src, 0o755)
	s.env["BERTHIER_SKIPLI_BIN"] = filepath.Join(dir, "skipli-cli")
	s.env["BERTHIER_BOARD_POLL"] = "0"
	return dir
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.WriteString(line + "\n")
}

func (s *sandbox) boardNews() (texts []string) {
	for _, a := range (&Env{Inbox: filepath.Join(s.home, "inbox")}).ReadInbox() {
		texts = append(texts, a.Kind+"|"+a.To+"|"+a.Text)
	}
	return texts
}

func (s *sandbox) clearInbox() {
	m, _ := filepath.Glob(filepath.Join(s.home, "inbox", "*.json"))
	for _, f := range m {
		os.Remove(f)
	}
}

func TestBoards(t *testing.T) {
	s := newSandbox(t)
	berthier := fakeHost(t, s, "takes")
	s.sim.Attach(berthier)
	sk := fakeSkipli(t, s)
	e := s.load()
	out := core.TickOut{State: core.State{Berthier: ptrTo(berthier)}}
	prompts := func() int { return len(s.host.CallsTo("Type")) }

	// --- adopt an existing board; a Corps without one gets a card on the Grand Army Board, created once
	appendLine(t, filepath.Join(sk, "boards"), "own1|Skipli")
	appendLine(t, filepath.Join(sk, "cards"), "own1|c-ui|UI")
	if msg, err := e.EnsureBoard("skipli", "own1"); err != nil || !strings.Contains(msg, "own Board 'Skipli' (own1)") {
		t.Fatalf("adopt: %q %v", msg, err)
	}
	if _, err := e.EnsureBoard("skipli", "nope"); err == nil || !strings.Contains(err.Error(), "cannot adopt") {
		t.Errorf("adopting an unknown board: %v", err)
	}
	if _, err := e.EnsureBoard(".dotfiles", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.EnsureBoard("learning", ""); err != nil {
		t.Fatal(err)
	}
	if msg, _ := e.EnsureBoard(".dotfiles", ""); !strings.Contains(msg, "already has a Board") {
		t.Errorf("second board call: %q", msg)
	}
	st := e.loadBoards()
	eq(t, "boards recorded", []any{st.Corps["skipli"], st.Corps["dotfiles"].Board, st.Corps["dotfiles"].Card != "", st.Corps["learning"].Board == st.Shared}, "[{own1 } ga-2 true true]")
	b, _ := os.ReadFile(filepath.Join(sk, "boards"))
	eq(t, "created once", strings.Count(string(b), "|Grand Army"), 1)
	eq(t, "card named after the label", strings.Contains(readFile(filepath.Join(sk, "cards")), "|.dotfiles"), true)

	// --- the first poll seeds silently
	dot := st.Corps["dotfiles"].Card
	appendLine(t, filepath.Join(sk, "tasks"), dot+"|t-old|already there|pending")
	appendLine(t, filepath.Join(sk, "tasks"), "c-ui|t-ui0|old ui|pending")
	e.pollBoards(out)
	eq(t, "seed is silent", s.boardNews(), "[]")

	// --- new work is queued once; the Corps's Marshal gets it, nothing else does
	appendLine(t, filepath.Join(sk, "tasks"), dot+"|t-new|fix the unit|pending")
	appendLine(t, filepath.Join(sk, "tasks"), "c-ui|t-ui1|dark mode|pending")
	appendLine(t, filepath.Join(sk, "cards"), "own1|c-new|Mobile")
	appendLine(t, filepath.Join(sk, "cards"), e.loadBoards().Shared+"|c-other|learning stuff") // a card on the shared board: Berthier's, no news
	e.pollBoards(out)
	eq(t, "news", strings.Join(s.boardNews(), "\n"), `board|dotfiles|[board] new task t-new "fix the unit" on Grand Army
board|skipli|[board] new card c-new "Mobile" on Skipli
board|skipli|[board] new task t-ui1 "dark mode" on Skipli`)
	s.clearInbox()
	e.pollBoards(out)
	eq(t, "news only once", s.boardNews(), "[]")

	// --- edits, moves and deletes are not news
	rewrite(t, filepath.Join(sk, "tasks"), func(l string) string {
		return strings.Replace(strings.Replace(l, "c-ui|t-ui1|dark mode|pending", "c-new|t-ui1|dark mode|done", 1), "fix the unit", "fix units", 1)
	})
	e.pollBoards(out)
	eq(t, "moves and edits are quiet", s.boardNews(), "[]")

	// --- more than ten pending tasks on a card: Berthier is told once
	for i := 0; i < 11; i++ {
		appendLine(t, filepath.Join(sk, "tasks"), dot+"|t-b"+string(rune('a'+i))+"|bulk|pending")
	}
	s.clearInbox()
	e.pollBoards(out)
	eq(t, "suggestion once", prompts(), 1)
	if txt := s.sim.Took(berthier); !strings.Contains(txt, "dotfiles") || !strings.Contains(txt, "own Board") {
		t.Errorf("suggestion text: %q", txt)
	}
	e.pollBoards(out)
	eq(t, "not repeated", prompts(), 1)

	s.clearInbox()

	// --- no Session: Berthier is told once, not per poll; polling resumes when it clears
	os.WriteFile(filepath.Join(sk, "expired"), nil, 0o644)
	e.pollBoards(out)
	e.pollBoards(out)
	eq(t, "session alert once", prompts(), 2)
	if txt := s.sim.Took(berthier); !strings.Contains(txt, "Session") || !strings.Contains(txt, "never run login") {
		t.Errorf("session text: %q", txt)
	}
	os.Remove(filepath.Join(sk, "expired"))
	appendLine(t, filepath.Join(sk, "tasks"), dot+"|t-late|added meanwhile|pending")
	e.pollBoards(out)
	eq(t, "work added while signed out arrives", s.boardNews(), "[board|dotfiles|[board] new task t-late \"added meanwhile\" on Grand Army]")
	os.WriteFile(filepath.Join(sk, "expired"), nil, 0o644)
	e.pollBoards(out)
	eq(t, "alert re-arms after recovery", prompts(), 3)

	// --- any other failure is escalated once it has lasted BoardFailAfter, and re-arms
	os.Remove(filepath.Join(sk, "expired"))
	base := time.Now().Unix() + 1000
	at := base
	e.Now = func() time.Time { return time.Unix(at, 0) }
	e.BoardFailAfter = 300
	os.WriteFile(filepath.Join(sk, "broken"), nil, 0o644)
	for _, at = range []int64{base, base + 60, base + 299} {
		e.pollBoards(out)
	}
	eq(t, "a blip is not escalated", prompts(), 3)
	os.Remove(filepath.Join(sk, "broken"))
	at = base + 300
	e.pollBoards(out)
	os.WriteFile(filepath.Join(sk, "broken"), nil, 0o644)
	at = base + 360
	e.pollBoards(out)
	at = base + 600
	e.pollBoards(out)
	eq(t, "a success resets the run", prompts(), 3)
	for _, at = range []int64{base + 660, base + 661, base + 700} {
		e.pollBoards(out)
	}
	eq(t, "escalated once after 5 minutes", prompts(), 4)
	if txt := s.sim.Took(berthier); !strings.Contains(txt, "not read for 5m") || !strings.Contains(txt, "exec: not found") {
		t.Errorf("error text: %q", txt)
	}
	os.Remove(filepath.Join(sk, "broken"))
	at = base + 800
	e.pollBoards(out)
	os.WriteFile(filepath.Join(sk, "broken"), nil, 0o644)
	for _, at = range []int64{base + 900, base + 1300} {
		e.pollBoards(out)
	}
	eq(t, "error alert re-arms", prompts(), 5)
	os.Remove(filepath.Join(sk, "broken"))

	// --- polled at most every BERTHIER_BOARD_POLL seconds
	os.Remove(filepath.Join(sk, "expired"))
	e.BoardPoll = 3600
	before := readFile(filepath.Join(sk, "calls"))
	e.pollBoards(out)
	eq(t, "throttled", readFile(filepath.Join(sk, "calls")), before)
}

func TestBoardAdoptSeedsSilently(t *testing.T) {
	s := newSandbox(t)
	sk := fakeSkipli(t, s)
	e := s.load()
	appendLine(t, filepath.Join(sk, "boards"), "own1|Skipli")
	appendLine(t, filepath.Join(sk, "cards"), "own1|c1|UI")
	appendLine(t, filepath.Join(sk, "tasks"), "c1|t1|one|pending")
	e.EnsureBoard("skipli", "own1")
	e.pollBoards(core.TickOut{})
	appendLine(t, filepath.Join(sk, "tasks"), "c1|t2|two|pending")
	e.EnsureBoard("skipli", "own1") // adopted again: the next poll seeds, no flood
	e.pollBoards(core.TickOut{})
	eq(t, "re-adopt seeds", s.boardNews(), "[]")
}

func TestBoardCommand(t *testing.T) {
	s := newSandbox(t)
	fakeSkipli(t, s)
	for _, c := range []struct {
		args []string
		err  string
	}{
		{nil, "usage: berthier board"},
		{[]string{"--adopt", "x"}, "usage: berthier board"},
		{[]string{"skipli", "--adopt"}, "usage: berthier board"},
		{[]string{"berthier"}, "not a Corps"},
		{[]string{"nobody"}, "no Corps 'nobody'"},
	} {
		_, errs, code := s.sh("", append([]string{"board"}, c.args...)...)
		if code != 1 || !strings.Contains(errs, c.err) {
			t.Errorf("board %v: %d %q", c.args, code, errs)
		}
	}
}

func ptrTo[T any](v T) *T { return &v }

func readFile(p string) string { b, _ := os.ReadFile(p); return string(b) }

func rewrite(t *testing.T, p string, f func(string) string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(f(readFile(p))), 0o644); err != nil {
		t.Fatal(err)
	}
}
