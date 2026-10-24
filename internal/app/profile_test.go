package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"berthier/internal/core"
)

func TestProfileFold(t *testing.T) {
	s := newSandbox(t)
	s.write("state.json", stateJSON)
	now := float64(time.Now().Unix())
	s.spend("a", "/r/skipli", now-100, 2)
	s.spend("c", "/r/dot", now-90, 1)
	s.spend("b", "/r/berthier", now-80, 0.5)
	// A subagent of session a.
	sub := filepath.Join(s.home, "claude", "projects", "p", "a", "subagents")
	os.MkdirAll(sub, 0o755)
	os.Rename(filepath.Join(s.home, "claude", "projects", "p", "c.jsonl"), filepath.Join(sub, "agent-x.jsonl"))
	s.env["BERTHIER_ROOT"] = "/r/berthier"
	out, errs, code := s.sh("", "profile")
	eq(t, "profile runs", errs+string(rune('0'+code)), "0")
	eq(t, "corps", jq(t, out, "corps.0.name")+" "+jq(t, out, "corps.0.totals.usd")+" "+jq(t, out, "corps.1.name"), `"skipli" 2 "dotfiles"`)
	eq(t, "Command overhead", jq(t, out, "overhead.totals.usd"), "0.5")
	eq(t, "subagent session", jq(t, out, "corps.1.divisions.0.sessions.0.id")+" "+jq(t, out, "corps.1.divisions.0.sessions.0.origin"), `"a/agent-x" "subagent"`)

	// Only appended lines are read; a line still being written waits.
	f, _ := os.OpenFile(filepath.Join(s.home, "claude", "projects", "p", "a.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"user","cwd":"/r/skipli","timestamp":"`)
	f.Close()
	out, _, _ = s.sh("", "profile")
	eq(t, "partial line waits", jq(t, out, "corps.0.totals.usd"), "2")
	b, _ := os.ReadFile(filepath.Join(s.home, "claude", "projects", "p", "a.jsonl"))
	os.WriteFile(filepath.Join(s.home, "claude", "projects", "p", "a.jsonl"), b[:strings.LastIndexByte(string(b), '\n')+1], 0o644)
	s.spend("a", "/r/skipli", now-50, 1)
	out, _, _ = s.sh("", "profile")
	eq(t, "appended turn counted once", jq(t, out, "corps.0.totals.usd")+" "+jq(t, out, "corps.0.totals.turns"), "3 2")
	turns := filepath.Join(s.home, "profile", "turns.jsonl")
	before, _ := os.ReadFile(turns)
	s.sh("", "profile")
	after, _ := os.ReadFile(turns)
	eq(t, "nothing new, nothing written", len(after), len(before))

	out, _, _ = s.sh("", "profile", "--session", "a")
	eq(t, "a session's turns, its subagent's among them", jq(t, out, "turns.0.id")+" "+jq(t, out, "turns.1.id")+" "+jq(t, out, "turns.2.id"), `"a:1" "a/agent-x:1" "a:2"`)
	out, _, _ = s.sh("", "profile", "--waste")
	eq(t, "waste only", jq(t, out, "waste"), "[]")
	_, errs, code = s.sh("", "profile", "--week", "x")
	eq(t, "bad args", errs+string(rune('0'+code)), "berthier: usage: berthier profile [--waste] [--corps <corps>] [--session <id>] [--week <weeks ago>]\n1")

	// Weeks older than kept move to per-session totals.
	s.spend("old", "/r/dot", now-30*86400, 4)
	os.Remove(filepath.Join(s.home, "profile", "cursor.json"))
	s.env["BERTHIER_PROFILE_WEEKS"] = "8"
	s.sh("", "profile")
	s.env["BERTHIER_PROFILE_WEEKS"] = "1"
	s.sh("", "profile")
	eq(t, "compacted", strings.Contains(s.read("profile/turns.jsonl"), `"old:1"`), false)
	eq(t, "archived", jq(t, s.read("profile/archive.json"), "old.usd")+" "+jq(t, s.read("profile/archive.json"), "old.corps"), `4 "dotfiles"`)
}

func TestLanded(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "init")
	git("checkout", "-q", "-b", "dev")
	git("commit", "-q", "--allow-empty", "-m", "main work")
	// dark-mode lands by a merge commit, fast-mode by fast-forward, share by a
	// squash-merged pull request (a new commit no branch has).
	git("checkout", "-q", "-b", "dark-mode")
	git("commit", "-q", "--allow-empty", "-m", "d1")
	git("commit", "-q", "--allow-empty", "-m", "d2")
	git("checkout", "-q", "dev")
	git("merge", "-q", "--no-ff", "--no-edit", "dark-mode")
	git("checkout", "-q", "-b", "fast-mode")
	git("commit", "-q", "--allow-empty", "-m", "f1")
	fast := git("rev-parse", "HEAD")
	git("checkout", "-q", "dev")
	git("merge", "-q", "--ff-only", "fast-mode")
	git("commit", "-q", "--allow-empty", "-m", "Share dialog (#28)")
	// The Marshal works mostly on master and is now on a feature branch
	// grown from dev: dev, where pull requests and merges land and which it
	// worked more than the feature branch, is the integration branch.
	git("branch", "master", "main")
	git("checkout", "-q", "-b", "fix/unmerged")
	git("commit", "-q", "--allow-empty", "-m", "not landed")

	turns := []core.Turn{
		{Corps: "skipli", Branch: "dev"}, {Corps: "skipli", Branch: "dev"}, {Corps: "skipli", Branch: "fix/unmerged"},
		{Corps: "skipli", Division: "dark-mode", Branch: "dark-mode"},
		{Corps: "skipli", Division: "fast-mode", Branch: "fast-mode", Ships: []string{"commit " + fast[:7]}},
		{Corps: "skipli", Division: "share", Branch: "feat/share", Ships: []string{"pr 28"}},
	}
	turns = append(turns, core.Turn{Corps: "skipli", Branch: "master"}, core.Turn{Corps: "skipli", Branch: "master"}, core.Turn{Corps: "skipli", Branch: "master"})
	br := mainBranches(turns)
	eq(t, "the Marshal's branches, most worked first", br["skipli"], []string{"master", "dev", "fix/unmerged"})
	got := []string{}
	for _, x := range core.Credit(landedOn(repo, br["skipli"], "skipli", "skipli", 0), turns) {
		got = append(got, fmt.Sprintf("%s/%s %d", x.Corps, x.Division, x.Units()))
	}
	eq(t, "every way a Division lands is credited to it", strings.Join(got, ","),
		"skipli/share 2,skipli/fast-mode 1,skipli/dark-mode 3,skipli/ 1,skipli/ 1")
	// With no pull requests or merges anywhere, the most worked branch wins,
	// not an old branch it grew from.
	plain := t.TempDir()
	for _, a := range [][]string{{"init", "-q"}, {"commit", "-q", "--allow-empty", "-m", "a"}, {"branch", "old"}, {"commit", "-q", "--allow-empty", "-m", "b"}} {
		exec.Command("git", append([]string{"-C", plain, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, a...)...).Run()
	}
	eq(t, "no landings: most worked", landedOn(plain, []string{"main", "old"}, "dotfiles", "dotfiles", 0)[0].Subject, "b")
	b := landedIn(repo, "", "grand-army", core.Overhead, 0)
	eq(t, "no branch known: HEAD", b[0].Subject, "not landed")
}
