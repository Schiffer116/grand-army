package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// sandbox is a BERTHIER_HOME in a temp dir; sh runs a command in it.
type sandbox struct {
	t    *testing.T
	home string
	env  map[string]string
}

func newSandbox(t *testing.T) *sandbox {
	return &sandbox{t, t.TempDir(), map[string]string{"BERTHIER_PROMPT_POLL": "0.001"}}
}

func (s *sandbox) getenv(k string) string {
	if k == "BERTHIER_HOME" {
		return s.home
	}
	if v, ok := s.env[k]; ok {
		return v
	}
	switch k {
	case "HOME":
		return s.home
	case "CLAUDE_CONFIG_DIR": // never the user's own transcripts
		return filepath.Join(s.home, "claude")
	}
	return ""
}

// spend appends a turn to a session's transcript in the sandbox's Claude
// directory: a prompt, then a reply on claude-opus-5-5 that Spent usd ($20
// per million output tokens) at ts.
func (s *sandbox) spend(session, cwd string, ts, usd float64) {
	s.t.Helper()
	dir := filepath.Join(s.home, "claude", "projects", "p")
	os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, session+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		s.t.Fatal(err)
	}
	defer f.Close()
	at := time.UnixMilli(int64(ts * 1000)).UTC().Format(time.RFC3339Nano)
	fmt.Fprintf(f, `{"type":"user","sessionId":%q,"cwd":%q,"timestamp":%q,"message":{"role":"user","content":"go"}}`+"\n", session, cwd, at)
	fmt.Fprintf(f, `{"type":"assistant","sessionId":%q,"cwd":%q,"timestamp":%q,"message":{"id":"m%v","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":0,"output_tokens":%d}}}`+"\n",
		session, cwd, at, ts, int64(usd/20*1e6+0.5))
}

// ledger appends a quota row as `berthier sample` writes it, at ts.
func (s *sandbox) ledger(ts float64, session string, week float64, resets ...float64) {
	r := 1800000000.0
	if len(resets) > 0 {
		r = resets[0]
	}
	f, _ := os.OpenFile(filepath.Join(s.home, "ledger.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	defer f.Close()
	fmt.Fprintf(f, `{"ts":%v,"session":%q,"cwd":"/x","cost":0,"week_pct":%v,"resets_at":%.0f}`+"\n", ts, session, week, r)
}

// sh runs a command; the result is stdout and stderr, then the exit status.
func (s *sandbox) sh(stdin string, args ...string) (stdout, stderr string, code int) {
	var out, errb bytes.Buffer
	code = Run(args, IO{strings.NewReader(stdin), &out, &errb}, s.getenv)
	return out.String(), errb.String(), code
}

func (s *sandbox) write(name, content string) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.home, name), []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *sandbox) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(s.home, name))
	return string(b)
}

func eq(t *testing.T, name string, got, want any) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%s\n  got:  %v\n  want: %v", name, got, want)
	}
}

func jq(t *testing.T, s, path string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("not JSON: %q", s)
	}
	for _, k := range strings.Split(path, ".") {
		if k == "" {
			continue
		}
		if l, ok := v.([]any); ok {
			i, err := strconv.Atoi(k)
			if err != nil || i >= len(l) {
				return "null"
			}
			v = l[i]
			continue
		}
		m, ok := v.(map[string]any)
		if !ok {
			return "null"
		}
		v = m[k]
	}
	b, _ := json.Marshal(v)
	return string(b)
}

const stateJSON = `{"berthier": "b:p1", "agents": {
  "a:p1": {"status": "idle", "workspace_label": "skipli", "cwd": "/r/skipli"},
  "d:p1": {"status": "idle", "workspace_label": "dark-mode", "cwd": "/h/dark-mode/src"},
  "c:p1": {"status": "idle", "workspace_label": ".dotfiles", "cwd": "/r/dot"},
  "b:p1": {"status": "idle", "workspace_label": "berthier", "cwd": "/r/berthier"},
  "o:p1": {"status": "gone", "workspace_label": "old"}},
 "corps": [{"name": "skipli", "repo_root": "/r/skipli", "divisions": [{"name": "dark-mode", "path": "/h/dark-mode"}]},
           {"name": ".dotfiles", "repo_root": "/r/dot", "divisions": []}]}`

func status(session, cwd string, cost, week float64, resets ...float64) string {
	r := 1800000000.0
	if len(resets) > 0 {
		r = resets[0]
	}
	return fmt.Sprintf(`{"session_id":%q,"cwd":%q,"cost":{"total_cost_usd":%v},"rate_limits":{"seven_day":{"used_percentage":%v,"resets_at":%.0f}}}`, session, cwd, cost, week, r)
}

func TestSample(t *testing.T) {
	s := newSandbox(t)
	s.write("state.json", stateJSON)
	s.write("divisions.json", `{"dark-mode": {"corps": "skipli", "worktree": true}}`)
	out, errs, code := s.sh(status("s1", "/r/skipli", 1, 10), "sample")
	eq(t, "sample is silent and succeeds", fmt.Sprintf("%q %q %d", out, errs, code), `"" "" 0`)
	eq(t, "sample row", jq(t, strings.TrimSpace(s.read("ledger.jsonl")), "session")+jq(t, strings.TrimSpace(s.read("ledger.jsonl")), "week_pct"), `"s1"10`)
	s.sh(status("s1", "/r/skipli", 1, 10), "sample")
	eq(t, "unchanged render adds nothing", strings.Count(s.read("ledger.jsonl"), "\n"), 1)
	s.sh(status("s1", "/r/skipli", 1.5, 10), "sample")
	eq(t, "cost change adds a row", strings.Count(s.read("ledger.jsonl"), "\n"), 2)
	_, errs, code = s.sh("not json", "sample")
	eq(t, "garbage never fails", fmt.Sprintf("%q %d", errs, code), `"" 0`)
	s.sh(`{"session_id":"s9","cwd":"/x"}`, "sample")
	s.sh(status("../x", "/x", 1, 1), "sample")
	eq(t, "no quota data or unsafe session id, no row", strings.Count(s.read("ledger.jsonl"), "\n"), 2)

	os.Chmod(filepath.Join(s.home, "ledger.jsonl"), 0)
	_, errs, code = s.sh(status("s2", "/x", 1, 1), "sample")
	os.Chmod(filepath.Join(s.home, "ledger.jsonl"), 0o644)
	eq(t, "unwritable ledger never fails", fmt.Sprintf("%q %d", errs, code), `"" 0`)

	os.Remove(filepath.Join(s.home, "ledger.jsonl"))
	os.RemoveAll(filepath.Join(s.home, "sampled"))
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.sh(status(fmt.Sprintf("c%d", i), "/x", float64(i+1), 5), "sample")
		}()
	}
	wg.Wait()
	rows := 0
	for _, l := range strings.Split(strings.TrimSpace(s.read("ledger.jsonl")), "\n") {
		if jq(t, l, "session") != "null" {
			rows++
		}
	}
	eq(t, "concurrent rows whole", rows, 40)
}

func TestBudgetFlow(t *testing.T) {
	s := newSandbox(t)
	s.write("state.json", stateJSON)
	s.write("divisions.json", `{"dark-mode": {"corps": "skipli", "worktree": true}}`)
	// Each rise in week_pct is split by the transcript Spend since the last.
	base := float64(time.Now().Unix() - 7200)
	s.ledger(base, "a", 40)
	s.spend("a", "/r/skipli", base+5, 3)
	s.ledger(base+10, "a", 41) // +1: skipli
	s.spend("d", "/h/dark-mode/src", base+15, 1)
	s.spend("c", "/r/dot", base+16, 1)
	s.ledger(base+20, "c", 43) // +2: skipli (its Division) and dotfiles
	s.spend("a", "/r/skipli", base+25, 1)
	s.ledger(base+30, "a", 44) // +1: skipli
	s.spend("c", "/r/dot", base+35, 2)
	s.ledger(base+40, "c", 46) // +2: dotfiles
	s.sh("", "budget")
	eq(t, "split by corps", jq(t, s.read("usage.json"), "by_corps"), `{"dotfiles":3,"skipli":3}`)
	eq(t, "week tracked", jq(t, s.read("usage.json"), "week_pct")+jq(t, s.read("usage.json"), "resets_at"), "461800000000")
	s.sh("", "budget")
	eq(t, "folded rows are not folded twice", jq(t, s.read("usage.json"), "by_corps.skipli"), "3")

	// A new window starts over.
	s.spend("a", "/r/skipli", base+45, 1)
	s.ledger(base+50, "a", 3, 1900000000)
	s.sh("", "budget")
	eq(t, "new window resets", jq(t, s.read("usage.json"), "resets_at")+jq(t, s.read("usage.json"), "week_pct"), "19000000003")

	// A record attributed the old way is refolded from the whole ledger once.
	s.write("usage.json", `{"by_corps":{"other":3},"offset":999999}`)
	s.sh("", "budget")
	eq(t, "old record refolded", jq(t, s.read("usage.json"), "by_corps")+jq(t, s.read("usage.json"), "source"), `{"skipli":3}"transcripts"`)

	// budget: set, show, drop.
	out, _, _ := s.sh("", "budget", "skipli", "10")
	eq(t, "budget set", out, "budget for skipli: 10% of the week\n")
	s.sh("", "budget", ".Dotfiles", "5")
	eq(t, "budgets file", jq(t, s.read("budgets.json"), "skipli")+jq(t, s.read("budgets.json"), "dotfiles"), "105")
	_, errs, code := s.sh("", "budget", "nope", "5")
	eq(t, "budget unknown corps", errs, "berthier: no Corps 'nope'; known: berthier dotfiles skipli \n")
	eq(t, "exit status", code, 1)
	_, errs, _ = s.sh("", "budget", "skipli", "0")
	eq(t, "budget bad pct", errs, "berthier: pct must be a number in (0, 100], or off: usage: berthier budget [<corps> <pct>|off]\n")
	_, errs, _ = s.sh("", "budget", "skipli", "lots")
	eq(t, "budget bad pct 2", errs[:30], "berthier: pct must be a number")
	t.Setenv("TZ", "UTC")
	time.Local = time.UTC
	shown, _, _ := s.sh("", "budget")
	lines := strings.Split(strings.TrimSpace(shown), "\n")
	eq(t, "budget header", lines[0], "week resets "+time.Unix(1900000000, 0).UTC().Format("Mon 02 Jan 15:04")+", account at 3%")
	eq(t, "budget rows", strings.Join(lines[1:], "|"), "corps     used  budget  of budget|skipli    3%    10%     30%|dotfiles  0%    5%      0%")
	s.sh("", "budget", "dotfiles", "off")
	eq(t, "budget dropped", jq(t, s.read("budgets.json"), "dotfiles"), "null")
	_, errs, _ = s.sh("", "budget", "dotfiles", "off")
	eq(t, "budget drop unknown", errs, "berthier: no budget set for 'dotfiles'\n")
}

func TestBaselineOnlyAndFiveHour(t *testing.T) {
	s := newSandbox(t)
	s.write("state.json", `{"agents": {}}`)
	s.sh(strings.Replace(status("r1", "/r/dot", 1, 73), `"resets_at":1800000000`, `"resets_at":null`, 1), "sample")
	out, _, _ := s.sh("", "budget")
	eq(t, "baseline-only shows", strings.SplitN(out, "\n", 2)[0], "week resets at an unknown time, account at 73%")
	army, _, _ := s.sh("", "army")
	eq(t, "baseline-only view in army", jq(t, army, "budget.week_pct")+jq(t, army, "budget.corps"), "73[]")

	f := t.TempDir()
	s2 := &sandbox{t, f, s.env}
	s2.write("state.json", `{"agents": {}}`)
	five := `{"session_id": "f1", "cwd": "/r/dot", "cost": {"total_cost_usd": 1}, "rate_limits": {"seven_day": {"used_percentage": 5, "resets_at": 1800000000}, "five_hour": {"used_percentage": 31, "resets_at": 1799990000}}}`
	s2.sh(five, "sample")
	eq(t, "five-hour in row", jq(t, strings.TrimSpace(s2.read("ledger.jsonl")), "five_pct")+jq(t, strings.TrimSpace(s2.read("ledger.jsonl")), "five_resets_at"), "311799990000")
	s2.sh("", "budget")
	army, _, _ = s2.sh("", "army")
	eq(t, "five-hour in view", jq(t, army, "budget.five_hour"), `{"pct":31,"resets_at":1799990000}`)
	s2.write("ledger.jsonl", s2.read("ledger.jsonl")+`{"ts":1,"session":"f1","cwd":"/r/dot","cost":2,"week_pct":6,"resets_at":1800000000}`+"\n")
	s2.sh("", "budget")
	army, _, _ = s2.sh("", "army")
	eq(t, "row without five-hour folds, its week_pct ignored", jq(t, army, "budget.week_pct")+jq(t, army, "budget.five_hour.pct"), "531")
	s2.sh(strings.Replace(five, `"used_percentage": 31`, `"used_percentage": 40`, 1), "sample")
	eq(t, "five-hour unchanged does not add a row", strings.Count(s2.read("ledger.jsonl"), "\n"), 2)
}

func TestLedgerRotation(t *testing.T) {
	s := newSandbox(t)
	s.write("state.json", stateJSON)
	s.env["BERTHIER_LEDGER_MAX"] = "300"
	base := float64(time.Now().Unix() - 7200)
	for i := 1; i <= 5; i++ {
		s.spend("r", "/r/dot", base+float64(i*10)-5, 1)
		s.ledger(base+float64(i*10), "r", float64(i+10))
	}
	s.sh("", "budget")
	_, prev := os.Stat(filepath.Join(s.home, "ledger.jsonl.prev"))
	_, cur := os.Stat(filepath.Join(s.home, "ledger.jsonl"))
	eq(t, "ledger rotated", fmt.Sprint(prev == nil, " ", cur == nil, " ", jq(t, s.read("usage.json"), "offset")), "true false 0")
	s.spend("r", "/r/dot", base+55, 1)
	s.ledger(base+60, "r", 20)
	s.sh("", "budget")
	eq(t, "rotation loses nothing", jq(t, s.read("usage.json"), "by_corps.dotfiles"), "9")
}

func TestCounts(t *testing.T) {
	s := newSandbox(t)
	out, _, _ := s.sh("", "counts")
	eq(t, "no state", out, `{"available":false,"working":0,"blocked":0,"done":0,"idle":0,"unknown":0,"gone":0,"frozen":0,"report":""}`+"\n")
	s.write("state.json", `{"berthier": "b:p1",
 "corps": [{"name": "skipli", "agents": ["a:p1"], "divisions": [{"name": "dark-mode", "worktree": true, "agents": ["d:p1"]}]},
           {"name": ".dotfiles", "agents": ["c:p1"], "divisions": []}],
 "agents": {
  "a:p1": {"status": "working", "session_id": "s-a", "workspace_label": "skipli"},
  "b:p1": {"status": "idle", "session_id": "s-b", "workspace_label": "berthier"},
  "c:p1": {"status": "idle", "session_id": "s-c", "workspace_label": ".dotfiles"},
  "d:p1": {"status": "blocked", "session_id": "s-d", "workspace_label": "dark-mode"}}}`)
	out, _, _ = s.sh("", "counts")
	eq(t, "fresh counts", jq(t, out, "available")+jq(t, out, "working")+jq(t, out, "blocked")+jq(t, out, "idle"), "true112")
	os.MkdirAll(filepath.Join(s.home, "reports"), 0o755)
	s.write("reports/s-a.json", `{"line": "building offline sync"}`)
	s.write("reports/s-c.json", `{"line": "Needs you: push main?"}`)
	s.write("reports/s-b.json", `{"line": "watching the Grand Army"}`)
	s.write("reports/s-gone.json", `{"line": "an old session"}`)
	out, _, _ = s.sh("", "counts")
	var c struct{ Report string }
	json.Unmarshal([]byte(out), &c)
	eq(t, "report", c.Report, "dark-mode: needs you - no report yet\ndotfiles: needs you - push main?\nberthier: idle - watching the Grand Army\nskipli: working - building offline sync")
	old := time.Now().Add(-5 * time.Minute)
	os.Chtimes(filepath.Join(s.home, "state.json"), old, old)
	out, _, _ = s.sh("", "counts")
	eq(t, "stale state", jq(t, out, "available")+jq(t, out, "idle"), "false2")
}

func TestReportAskHookLog(t *testing.T) {
	s := newSandbox(t)
	s.write("state.json", `{"berthier": "b:p1", "agents": {
  "a:p1": {"status": "idle", "workspace_label": "skipli"},
  "b:p1": {"status": "working", "workspace_label": "berthier"},
  "c:p1": {"status": "idle", "workspace_label": ".dotfiles"},
  "d:p1": {"status": "gone", "workspace_label": "old"}}}`)
	s.env["CLAUDE_CONFIG_DIR"] = filepath.Join(s.home, "claude")

	// --- report
	s.sh("", "report", "outside herdr")
	m, _ := filepath.Glob(filepath.Join(s.home, "reports", "*.json"))
	eq(t, "report no-op outside herdr", len(m), 0)
	s.env["HERDR_PANE_ID"], s.env["CLAUDE_CODE_SESSION_ID"] = "a:p1", "s-a"
	s.sh("", "report", "needs you:\tpush main?\n")
	eq(t, "report written", jq(t, s.read("reports/s-a.json"), "line")+jq(t, s.read("reports/s-a.json"), "pane"), `"needs you: push main?""a:p1"`)

	// A backgrounded fork's Report is also filed under the session it was
	// forked from (an ancestor process), which is the one Herdr and the Grand
	// Army State know.
	e := LoadEnv(s.getenv)
	os.MkdirAll(filepath.Join(e.ClaudeDir, "sessions"), 0o755)
	sessionFile := func(pid int, sid, kind string) {
		os.WriteFile(filepath.Join(e.ClaudeDir, "sessions", fmt.Sprint(pid)+".json"), []byte(fmt.Sprintf(`{"pid": %d, "sessionId": %q, "kind": %q}`, pid, sid, kind)), 0o644)
	}
	me := os.Getpid()
	sessionFile(os.Getppid(), "s-root", "interactive")
	sessionFile(me, "s-fork", "bg")
	eq(t, "fork chain", e.forkedFrom("s-fork", me), "[s-root]")
	sessionFile(me, "s-fork", "interactive")
	eq(t, "nested non-bg session stays its own", len(e.forkedFrom("s-fork", me)), 0)

	// --- hook-stop: never blocks; an unreported turn gets its report
	tr := filepath.Join(s.home, "t.jsonl")
	os.WriteFile(tr, []byte(`{"type":"user","timestamp":"2020-01-01T00:00:00.000Z","message":{"content":"old prompt"}}
{"type":"assistant","timestamp":"2020-01-01T00:00:01.000Z","message":{"content":[{"type":"text","text":"old answer"}]}}
{"type":"user","timestamp":"2099-01-01T00:00:00.000Z","message":{"content":"do the thing"}}
{"type":"assistant","timestamp":"2099-01-01T00:00:02.000Z","message":{"content":[{"type":"tool_use"}]}}
{"type":"user","timestamp":"2099-01-01T00:00:05.000Z","message":{"content":[{"type":"tool_result"}]}}
{"type":"assistant","timestamp":"2099-01-01T00:00:06.000Z","message":{"content":[{"type":"text","text":"Pushed the fix. Want me to open the PR?"}]}}
`), 0o644)
	hook := func(in string) string { out, _, _ := s.sh(in, "hook-stop"); return strings.TrimSpace(out) }
	in := fmt.Sprintf(`{"session_id":"s-a","transcript_path":%q}`, tr)
	eq(t, "hook never blocks", hook(in), "")
	eq(t, "hook reports the unreported turn", jq(t, s.read("reports/s-a.json"), "line"), `"needs you: Pushed the fix."`)
	os.Remove(filepath.Join(s.home, "reports", "s-a.json"))
	delete(s.env, "HERDR_PANE_ID")
	eq(t, "hook ignores non-herdr", hook(in), "")
	eq(t, "hook writes nothing outside herdr", s.read("reports/s-a.json"), "")
	s.env["HERDR_PANE_ID"] = "a:p1"
	// An explicit report of the turn wins over the automatic one.
	os.WriteFile(tr, []byte(strings.ReplaceAll(strings.ReplaceAll(string(must(os.ReadFile(tr))), "2099-01-01T00:00:05", "2020-01-02T00:00:00"), "2099-01-01T00:00:00", "2020-01-02T00:00:00")), 0o644)
	s.sh("", "report", "pushed; green")
	eq(t, "hook keeps a reported turn", hook(in), "")
	eq(t, "explicit report kept", jq(t, s.read("reports/s-a.json"), "line"), `"pushed; green"`)

	// --- ask
	delete(s.env, "HERDR_PANE_ID")
	_, errs, _ := s.sh("", "ask", "skipli", "hi")
	eq(t, "ask needs a pane", errs, "berthier: run this from an Agent's Herdr pane (no HERDR_PANE_ID)\n")
	s.env["HERDR_PANE_ID"] = "a:p1"
	_, errs, _ = s.sh("", "ask", "old", "hi")
	eq(t, "ask unknown corps", errs, "berthier: no Corps 'old'; known: berthier dotfiles skipli \n")
	s.sh("", "ask", ".Dotfiles", "add a\nunit")
	m, _ = filepath.Glob(filepath.Join(s.home, "inbox", "*.json"))
	eq(t, "ask queued", jq(t, string(must(os.ReadFile(m[0]))), "from")+jq(t, string(must(os.ReadFile(m[0]))), "to")+jq(t, string(must(os.ReadFile(m[0]))), "text"), `"skipli""dotfiles""add a unit"`)
	// --- notify: no pane needed, one-way, capped, multi-line kept
	delete(s.env, "HERDR_PANE_ID")
	_, errs, _ = s.sh("", "notify", "old", "hi")
	eq(t, "notify unknown corps", errs, "berthier: no Corps 'old'\n")
	clear := func() {
		m, _ := filepath.Glob(filepath.Join(s.home, "inbox", "*.json"))
		for _, f := range m {
			os.Remove(f)
		}
	}
	clear()
	s.sh("", "notify", ".Skipli", "answer:\n42")
	m, _ = filepath.Glob(filepath.Join(s.home, "inbox", "*.json"))
	b0 := string(must(os.ReadFile(m[0])))
	eq(t, "notify queued", jq(t, b0, "kind")+jq(t, b0, "from")+jq(t, b0, "to")+jq(t, b0, "text"), `"notify""service""skipli""answer:\n42"`)
	clear()
	s.env["BERTHIER_NOTIFY_MAX"] = "5"
	s.sh("", "notify", "skipli", "abcdefgh")
	m, _ = filepath.Glob(filepath.Join(s.home, "inbox", "*.json"))
	eq(t, "notify capped", jq(t, string(must(os.ReadFile(m[0]))), "text"), `"abcde [truncated]"`)
	clear()
	delete(s.env, "BERTHIER_NOTIFY_MAX")
	// --- shutdown / wake: one one-way file per live Agent, tagged
	out, _, _ := s.sh("", "shutdown", "back at 9")
	m, _ = filepath.Glob(filepath.Join(s.home, "inbox", "*.json"))
	b0 = string(must(os.ReadFile(m[0])))
	eq(t, "shutdown queued", []any{len(m) > 0, jq(t, b0, "kind"), strings.HasPrefix(jq(t, b0, "text"), `"[berthier shutdown] `), strings.Contains(jq(t, b0, "text"), "back at 9")}, []any{true, `"shutdown"`, true, true})
	if !strings.Contains(out, fmt.Sprintf("queued for %d Agents; 0 Frozen skipped", len(m))) {
		t.Errorf("shutdown output %q for %d files", out, len(m))
	}
	clear()
	s.sh("", "wake")
	m, _ = filepath.Glob(filepath.Join(s.home, "inbox", "*.json"))
	eq(t, "wake queued", jq(t, string(must(os.ReadFile(m[0]))), "kind"), `"wake"`)
	clear()
	s.env["HERDR_PANE_ID"] = "b:p1"
	s.sh("", "ask", "skipli", "all done")
	m, _ = filepath.Glob(filepath.Join(s.home, "inbox", "*.json"))
	froms := ""
	for _, f := range m {
		b := string(must(os.ReadFile(f)))
		if jq(t, b, "to") == `"skipli"` {
			froms = jq(t, b, "from")
		}
	}
	eq(t, "berthier asks as berthier", froms, `"berthier"`)
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestLoopOutageAndLog(t *testing.T) {
	s := newSandbox(t)
	s.write("loop.log", "2026-10-17T13:02:57+07:00 loop started (interval 10s, session default)\n")
	s.env["BERTHIER_INTERVAL"] = "0.05"
	s.env["BERTHIER_HERDR"] = "/nonexistent/herdr"
	e := LoadEnv(s.getenv)
	go e.Loop() // killed with the test binary; stopping needs a signal
	time.Sleep(400 * time.Millisecond)
	errors := 0
	for _, l := range strings.Split(s.read("loop.log"), "\n") {
		if jq0(l, "kind") == "loop_error" {
			errors++
			eq(t, "outage reason", jq0(l, "why"), "herdr unreachable")
		}
	}
	eq(t, "outage logged once", errors, 1)
	eq(t, "prose log set aside", s.read("loop.log.pre-json"), "2026-10-17T13:02:57+07:00 loop started (interval 10s, session default)\n")
	out, _, _ := s.sh("", "log", "-n", "1")
	eq(t, "log pretty-prints", len(strings.Fields(out)) > 2, true)
}

func jq0(line, key string) string {
	var m map[string]any
	if json.Unmarshal([]byte(line), &m) != nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

func TestDemo(t *testing.T) {
	s := newSandbox(t)
	s.write("state.json", `{"berthier": "b:p1", "corps": [{"name": "skipli", "agents": ["a:p1"], "divisions": []}], "agents": {"a:p1": {"status": "idle", "session_id": "s-a", "workspace_label": "skipli"}}}`)
	s.env["CLAUDE_CONFIG_DIR"] = filepath.Join(s.home, "claude")
	s.sh("", "demo", "http://localhost:5174")
	m, _ := filepath.Glob(filepath.Join(s.home, "demos", "*.json"))
	eq(t, "no-op outside herdr", len(m), 0)

	s.env["HERDR_PANE_ID"], s.env["CLAUDE_CODE_SESSION_ID"] = "a:p1", "s-a"
	s.sh("", "demo", "http://localhost:5174", "backfill\tprogress")
	s.sh("", "demo", "https://x.example/preview")
	s.sh("", "demo", "http://localhost:5174", "backfill v2") // same URL: updated in place, newest first
	out, _, _ := s.sh("", "demo", "--list")
	eq(t, "list", out, "http://localhost:5174\tbackfill v2\nhttps://x.example/preview\t-\n")
	for _, bad := range []string{"javascript:alert(1)", "localhost:5174", "ftp://h/x", "not a url"} {
		_, errs, code := s.sh("", "demo", bad)
		if code == 0 || !strings.Contains(errs, "demo url must be http(s)") {
			t.Errorf("demo %q: code %d stderr %q", bad, code, errs)
		}
	}
	s.sh("", "demo", "--clear", "http://localhost:5174")
	out, _, _ = s.sh("", "demo", "--list")
	eq(t, "clear one", out, "https://x.example/preview\t-\n")

	army, _, _ := s.sh("", "army")
	eq(t, "army view", jq(t, army, "corps.0.agents.0.demos.0.url"), `"https://x.example/preview"`)
	s.sh("", "demo", "--clear")
	out, _, _ = s.sh("", "demo", "--list")
	eq(t, "clear all", out, "")
}
