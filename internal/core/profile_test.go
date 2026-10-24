package core

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Transcript lines as Claude Code writes them.
func stamp(sec float64) string {
	return time.UnixMilli(int64(sec * 1000)).UTC().Format(time.RFC3339Nano)
}

func userLine(sec float64, text string) string {
	return fmt.Sprintf(`{"type":"user","cwd":"/r/skipli","gitBranch":"main","timestamp":%q,"message":{"role":"user","content":%q}}`, stamp(sec), text)
}

func resultLine(sec float64, id string, content string) string {
	return fmt.Sprintf(`{"type":"user","cwd":"/r/skipli","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":%q}]}}`, stamp(sec), id, content)
}

// call is one content block of an API call's reply: usage repeats on each.
func call(sec float64, msg, model string, in, cr, w5, w1, out int64, blocks string) string {
	return fmt.Sprintf(`{"type":"assistant","cwd":"/r/skipli","gitBranch":"main","timestamp":%q,"message":{"id":%q,"model":%q,"content":[%s],`+
		`"usage":{"input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,"output_tokens":%d,"cache_creation":{"ephemeral_5m_input_tokens":%d,"ephemeral_1h_input_tokens":%d}}}}`,
		stamp(sec), msg, model, blocks, in, cr, w5+w1, out, w5, w1)
}

func lines(l ...string) [][]byte {
	out := make([][]byte, len(l))
	for i, s := range l {
		out[i] = []byte(s)
	}
	return out
}

var skipliPlace = func(cwd string) (string, string, bool) {
	if strings.HasPrefix(cwd, "/r/skipli") {
		return "skipli", "", true
	}
	return "", "", false
}

func TestPrices(t *testing.T) {
	p, ok := PriceOf("claude-opus-5-5")
	check(t, "opus 5.5", []any{p.In, p.Out, p.CacheRead, ok}, `[4,20,0.2,true]`)
	_, ok = PriceOf("claude-haiku-4-5-20251001")
	check(t, "dated snapshot priced by prefix", ok, `true`)
	_, ok = PriceOf("claude-nova-9")
	check(t, "unknown model unpriced", ok, `false`)
	// 1M each of input, cache read, 5m write, 1h write, output on opus 5.5:
	// 4 + 0.2 + 5 + 8 + 20.
	all := Tokens{In: 1e6, CR: 1e6, W5m: 1e6, W1h: 1e6, Out: 1e6}
	check(t, "token kinds priced", round4(Prices["claude-opus-5-5"].USD(all, false)), `37.2`)
	check(t, "fast mode doubles", round4(Prices["claude-opus-5-5"].USD(all, true)), `74.4`)
	h := Prices["claude-haiku-5-5"]
	check(t, "haiku 5.5 short and long prompts", []any{round4(h.USD(Tokens{In: 1e5, Out: 1e6}, false)), round4(h.USD(Tokens{In: 1e5 + 1, Out: 1e6}, false))}, `[0.51,2.55]`)
}

func TestFoldTranscriptTurns(t *testing.T) {
	c, rows := FoldTranscript(Cursor{}, TranscriptFile{Session: "s1"}, lines(
		userLine(100, "fix the bug"),
		// One call whose reply has two blocks: its usage counts once.
		call(101, "m1", "claude-opus-5-5", 10, 0, 0, 1000, 50, `{"type":"text","text":"ok"}`),
		call(102, "m1", "claude-opus-5-5", 10, 0, 0, 1000, 50, `{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/r/skipli/a.go"}}`),
		resultLine(103, "t1", "package a"),
		call(104, "m2", "claude-opus-5-5", 1, 1060, 0, 20, 30, `{"type":"text","text":"done"}`),
		userLine(200, "[berthier-loop] Grand Army events:\n- x"),
		call(201, "m3", "claude-opus-5-5", 2, 1080, 0, 10, 5, `{"type":"text","text":"noted"}`),
	), skipliPlace)
	check(t, "two turns", len(rows), `2`)
	r := rows[0]
	check(t, "first turn", []any{r.ID, r.Corps, r.Origin, r.Prompt, r.Calls, r.Tools, r.In, r.CR, r.W1h, r.Out, r.CtxMax, r.Start, r.End},
		`["s1:1","skipli","user","fix the bug",2,1,11,1060,1020,80,1081,101,104]`)
	// 11 in * 4 + 1060 read * 0.2 + 1020 1h-write * 8 + 80 out * 20, per million.
	check(t, "first turn's dollars, in millionths", int64(r.USD*1e6+0.5), `10016`)
	check(t, "loop turn", []any{rows[1].ID, rows[1].Origin, rows[1].Calls}, `["s1:2","loop",1]`)
	check(t, "cursor", []any{c.Seq, c.Msg, c.Reads}, `[2,"m3",{"/r/skipli/a.go":1}]`)

	// The next pass continues the same turn: its row has the same key.
	c, rows = FoldTranscript(c, TranscriptFile{Session: "s1"}, lines(
		call(205, "m4", "claude-opus-5-5", 2, 1090, 0, 10, 5, ``),
	), skipliPlace)
	check(t, "continued turn", []any{rows[0].Key(), rows[0].Calls}, `["s1:2|claude-opus-5-5",1]`)
	merged := Turn{ID: "s1:2", Model: "claude-opus-5-5", Calls: 1, Start: 201, End: 201}
	merged.Merge(rows[0])
	check(t, "merge", []any{merged.Calls, merged.Start, merged.End}, `[2,201,205]`)
}

func TestFoldTranscriptOrigins(t *testing.T) {
	for _, tc := range []struct{ prompt, origin string }{
		{"hello", "user"},
		{"[berthier ask] from dotfiles: x", "ask"},
		{"[berthier notify] from service: x", "notify"},
		{"[berthier brief] Build the thing", "brief"},
		{"[berthier continue] The machine restarted", "continue"},
		{"[berthier resume] The usage limit has reset", "resume"},
		{"[berthier shutdown] The user is closing down", "shutdown"},
		{"[berthier wake] The army is back", "wake"},
		{"<task-notification>done</task-notification>", "task"},
	} {
		check(t, tc.prompt, OriginOf(tc.prompt), `"`+tc.origin+`"`)
	}
	check(t, "tagging", []string{Tag("[berthier brief]", "Build it"), Tag("[berthier brief]", "[berthier brief] Build it")}, `["[berthier brief] Build it","[berthier brief] Build it"]`)
	// Meta lines and local commands do not start turns; a subagent's turns are "subagent".
	_, rows := FoldTranscript(Cursor{}, TranscriptFile{Session: "p/agent-1", Parent: "p"}, lines(
		userLine(1, "look around"),
		call(2, "m1", "claude-haiku-5-5", 5, 0, 10, 0, 5, ``),
	), skipliPlace)
	check(t, "subagent", []any{rows[0].ID, rows[0].Parent, rows[0].Origin, rows[0].Prompt}, `["p/agent-1:1","p","subagent","look around"]`)
	_, rows = FoldTranscript(Cursor{}, TranscriptFile{Session: "s"}, lines(
		userLine(1, "<command-name>/clear</command-name>"),
		`{"type":"user","isMeta":true,"timestamp":"`+stamp(2)+`","message":{"content":"caveat"}}`,
		call(3, "m1", "claude-opus-5-5", 5, 0, 10, 0, 5, ``),
		call(4, "m2", "<synthetic>", 0, 0, 0, 0, 0, ``),
		call(5, "m3", "claude-nova-9", 5, 0, 0, 0, 5, ``),
	), func(string) (string, string, bool) { return "", "", false })
	check(t, "calls before any prompt are turn 1; unknown cwd is other; unknown model unpriced",
		[]any{rows[0].ID, rows[0].Origin, rows[0].Corps, len(rows), rows[1].Unpriced, rows[1].USD}, `["s:1","user","other",2,true,0]`)
}

func TestFoldTranscriptWaste(t *testing.T) {
	big := strings.Repeat("x", BigResult+4000)
	_, rows := FoldTranscript(Cursor{}, TranscriptFile{Session: "s"}, lines(
		userLine(0, "go"),
		call(1, "m1", "claude-opus-5-5", 1, 0, 0, 160000, 10, `{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/a"}}`),
		resultLine(2, "t1", big),
		call(3, "m2", "claude-opus-5-5", 1, 160000, 0, 1000, 10, `{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"herdr agent read w1:p2 --source visible"}}`),
		resultLine(4, "t2", "screen"),
		call(5, "m3", "claude-opus-5-5", 1, 161000, 0, 10, 10, `{"type":"tool_use","id":"t3","name":"Read","input":{"file_path":"/a"}}`),
		resultLine(6, "t3", "again"),
		// Back after two hours: the 1h cache expired and is written again.
		userLine(7206, "still there?"),
		call(7207, "m4", "claude-opus-5-5", 1, 3000, 0, 158000, 10, ``),
	), skipliPlace)
	r := rows[0]
	check(t, "bloat, oversized result, re-read", []any{r.Bloat, r.Big, r.BigTool, r.Reread, r.Rereads}, `[32013,11000,"Read",1,["/a"]]`)
	check(t, "cache miss after the TTL", []any{rows[1].Miss, rows[1].Bloat}, `[158000,11001]`)
}

func TestPlacer(t *testing.T) {
	st := &State{Berthier: ptr("b:p1"), Corps: []Corps{
		{Name: "skipli", RepoRoot: ptr("/r/skipli"), Divisions: []UnitState{{Name: "dark-mode", Path: ptr("/h/w/skipli/dark-mode")}}},
		{Name: ".dotfiles", RepoRoot: ptr("/r/dot")}},
		BerthierDivisions: []UnitState{{Name: "console", Path: ptr("/h/w/grand-army/console")}}}
	st.Agents.Set("a:p1", &Agent{Cwd: ptr("/r/skipli"), WorkspaceLabel: ptr("skipli")})
	st.Agents.Set("d:p1", &Agent{Cwd: ptr("/h/w/skipli/dark-mode/src"), WorkspaceLabel: ptr("dark-mode")})
	st.Agents.Set("b:p1", &Agent{Cwd: ptr("/r/grand-army"), WorkspaceLabel: ptr("grand-army")})
	p := NewPlacer(st, Divisions{"dark-mode": {Corps: "skipli"}}, "/r/grand-army")
	for _, tc := range []struct{ cwd, want string }{
		{"/r/skipli/web", `["skipli","",true]`},
		{"/h/w/skipli/dark-mode/src/x", `["skipli","dark-mode",true]`},
		{"/r/dot", `["dotfiles","",true]`},
		{"/r/grand-army", `["berthier","",true]`},                    // Command overhead
		{"/h/w/grand-army/console", `["grand-army","console",true]`}, // Berthier's Division: its Corps
		{"/u/.herdr/worktrees/skipli/old-feature", `["skipli","old-feature",true]`},
		{"/u/.herdr/worktrees/grand-army/go-port", `["grand-army","go-port",true]`},
		{"/r/skipli/.claude/worktrees/spike", `["skipli","spike",true]`},
		{"/r/grand-army/.claude/worktrees/spike", `["grand-army","spike",true]`},
		{"/elsewhere", `["","",false]`},
	} {
		c, d, ok := p.Place(tc.cwd)
		check(t, tc.cwd, []any{c, d, ok}, tc.want)
	}
}

func TestWeekStart(t *testing.T) {
	check(t, "anchored on resets_at", WeekStart(1000000, ptr(1000000.0+3*86400)), `654400`)
	check(t, "epoch weeks without one", WeekStart(Week*3+5, nil), fmt.Sprint(Week*3))
}

func turn(id, corps, div, model, origin string, start, usd float64) Turn {
	return Turn{ID: id, Session: strings.Split(id, ":")[0], Corps: corps, Division: div, Model: model, Origin: origin,
		Start: start, End: start + 60, Calls: 3, Tools: 3, Tokens: Tokens{In: 10, CR: 900, W1h: 100, Out: 5000}, USD: usd}
}

func TestProfileView(t *testing.T) {
	ws := 1000.0 * Week // the current week starts here
	now := ws + 3*86400
	resets := ws + Week
	turns := []Turn{
		turn("a:1", "skipli", "", "claude-opus-5-5", "user", ws+100, 4),
		turn("a:2", "skipli", "", "claude-opus-5-5", "ask", ws+86400+100, 2),
		turn("d:1", "skipli", "dark-mode", "claude-sonnet-5-5", "brief", ws+2*86400, 3),
		turn("b:1", Overhead, "", "claude-opus-5-5", "loop", ws+200, 1),
		turn("o:1", "dotfiles", "", "claude-opus-5-5", "user", ws-Week+5, 7), // last week
	}
	short := turn("a:3", "skipli", "", "claude-opus-5-5", "user", ws+300, 0.5)
	short.Calls, short.Tools, short.Out = 1, 0, 100
	turns = append(turns, short)
	landed := []Landed{
		{Corps: "skipli", Commits: 2, At: ws + 500},
		{Corps: "skipli", Division: "dark-mode", Commits: 3, Finished: true, At: ws + 3*86400 - 10},
		{Corps: "skipli", Commits: 9, At: ws - 10}, // last week
	}
	v := Profile(ProfileIn{Turns: turns, Landed: landed, Now: now, ResetsAt: &resets, WeekPct: ptr(30.0), ByCorps: map[string]float64{"skipli": 20}})
	check(t, "week", v.Week, fmt.Sprintf(`{"start":%v,"end":%v,"weeks_ago":0,"live":true}`, ws, ws+Week))
	check(t, "total", []any{v.Total.Totals.USD, v.Total.Totals.Turns, v.Total.Units, v.Total.Totals.CacheHit}, `[10.5,5,6,0.891]`)
	check(t, "corps without overhead", len(v.Corps), `1`)
	sk := v.Corps[0]
	check(t, "skipli", []any{sk.Name, sk.Totals.USD, sk.Units, sk.PerUnit, sk.QuotaPct, sk.Daily, sk.Origins},
		`["skipli",9.5,6,1.5833,20,[4.5,2,3,0,0,0,0],{"ask":2,"brief":3,"user":4.5}]`)
	check(t, "divisions: Main Agent and dark-mode, by Spend",
		[]any{sk.Divisions[0].Name, sk.Divisions[0].Units, sk.Divisions[0].PerUnit, sk.Divisions[1].Name, sk.Divisions[1].Units, sk.Divisions[1].PerUnit},
		`["",2,3.25,"dark-mode",4,0.75]`)
	check(t, "sessions", []any{len(sk.Divisions[0].Sessions), sk.Divisions[0].Sessions[0].ID, sk.Divisions[0].Sessions[0].Totals.Turns, sk.Divisions[0].Sessions[0].Model},
		`[1,"a",3,"claude-opus-5-5"]`)
	check(t, "Command overhead apart", []any{v.Overhead.Name, v.Overhead.Totals.USD}, `["berthier",1]`)
	check(t, "models", []any{v.Models["claude-opus-5-5"].USD, v.Models["claude-sonnet-5-5"].USD}, `[7.5,3]`)
	check(t, "waste: the short opus turn", []any{len(v.Waste), v.Waste[0].Kind, v.Waste[0].Turn}, `[1,"model","a:3"]`)
	check(t, "weeks, newest first", []any{len(v.Weeks), v.Weeks[0].USD, v.Weeks[1].ByCorps}, `[2,10.5,{"dotfiles":7}]`)

	last := Profile(ProfileIn{Turns: turns, Landed: landed, Now: now, ResetsAt: &resets, WeekPct: ptr(30.0), WeeksAgo: 1})
	check(t, "last week", []any{last.Week.Live, last.WeekPct, last.Corps[0].Name, last.Total.Units}, `[false,null,"dotfiles",9]`)
	one := Profile(ProfileIn{Turns: turns, Now: now, ResetsAt: &resets, Corps: "dark-mode", Session: "d"})
	check(t, "one Division, one session's turns", []any{one.Total.Totals.USD, len(one.Turns), one.Turns[0].ID}, `[3,1,"d:1"]`)
}

func TestWasteRanking(t *testing.T) {
	a := turn("a:1", "skipli", "", "claude-opus-5-5", "user", 0, 5)
	a.Bloat, a.CtxMax = 2000000, 300000 // 2M tokens over, read at $0.20/M: $0.40
	a.Miss = 100000                     // 1h writes at $8/M less a $0.20 read: $0.78
	b := turn("b:1", "dotfiles", "x", "claude-opus-5-5", "user", 0, 5)
	b.Big, b.BigTool = 50000, "Bash" // $0.40
	b.Reread, b.Rereads = 10000, []string{"pane w1:p2"}
	w := Waste([]Turn{a, b})
	got := []string{}
	for _, x := range w {
		got = append(got, fmt.Sprintf("%s %s %.2f", x.Kind, x.Turn, x.USD))
	}
	check(t, "ranked by dollars", got, `["cache_miss a:1 0.78","bloat a:1 0.40","big_result b:1 0.40","reread b:1 0.08"]`)
	check(t, "evidence", []any{w[1].Evidence, w[3].Evidence}, `["context peaked at 300k tokens; 2.0M tokens above 150k in 1 turn(s)","read again: pane w1:p2 (~10k tokens)"]`)
	check(t, "a Corps's line", WasteLine(w, "skipli", 3), `"cache_miss $0.78 (a:1), bloat $0.40 (a:1)"`)
}

func TestCredit(t *testing.T) {
	turns := []Turn{
		{Corps: "skipli", Division: "share", Branch: "feat/share", Ships: []string{"pr 28", "commit abc1234"}},
		{Corps: "skipli", Division: "fast", Branch: "fast", Ships: []string{"commit def5678"}},
		{Corps: "skipli", Division: "board", Branch: "ui/board"},
		{Corps: "skipli", Branch: "dev", Ships: []string{"commit 9999999"}}, // the Main Agent's own
		{Corps: "dotfiles", Division: "x", Ships: []string{"pr 5"}},
	}
	for _, tc := range []struct {
		l    Landed
		want string
	}{
		{Landed{Corps: "skipli", Main: "skipli", Commits: 1, Subject: "feat: share dialog (#28)", Sha: "1111111aaa"}, `["skipli","share",2]`},
		{Landed{Corps: "skipli", Main: "skipli", Commits: 1, Subject: "fix: thing (#5)", Sha: "2222222"}, `["skipli","",1]`}, // another repo's #5
		{Landed{Corps: "skipli", Main: "skipli", Commits: 3, Merge: true, Subject: "Merge pull request #28 from o/feat/share", Sha: "3333333"}, `["skipli","share",4]`},
		{Landed{Corps: "skipli", Main: "skipli", Commits: 2, Merge: true, Subject: "Merge pull request #40 from o/ui/board", Sha: "4444444"}, `["skipli","board",3]`},
		{Landed{Corps: "skipli", Main: "skipli", Commits: 1, Subject: "f1", Sha: "def5678ffff"}, `["skipli","fast",1]`},
		{Landed{Corps: "skipli", Main: "skipli", Commits: 2, Merge: true, Subject: "Merge branch 'ui/board'", Sha: "5555555"}, `["skipli","board",3]`},
		{Landed{Corps: "skipli", Main: "skipli", Commits: 2, Merge: true, Subject: "Merge branch 'Old-Div'", Sha: "6666666"}, `["skipli","old-div",3]`},
		{Landed{Corps: "skipli", Main: "skipli", Commits: 1, Subject: "main work", Sha: "9999999"}, `["skipli","",1]`},
		{Landed{Corps: "grand-army", Main: Overhead, Commits: 1, Subject: "notes", Sha: "7777777"}, `["berthier","",1]`},
		{Landed{Corps: "skipli", Division: "set", Commits: 1, Finished: true}, `["skipli","set",2]`},
	} {
		c := Credit([]Landed{tc.l}, turns)[0]
		check(t, tc.l.Subject, []any{c.Corps, c.Division, c.Units()}, tc.want)
	}
}

func TestFoldTranscriptShips(t *testing.T) {
	bash := func(id, cmd string) string {
		return fmt.Sprintf(`{"type":"tool_use","id":%q,"name":"Bash","input":{"command":%q}}`, id, cmd)
	}
	c, rows := FoldTranscript(Cursor{}, TranscriptFile{Session: "s"}, lines(
		userLine(0, "ship it"),
		call(1, "m1", "claude-sonnet-5-5", 1, 0, 10, 0, 5, bash("t1", "git add -A && git commit -q -m 'Share dialog'")),
		resultLine(2, "t1", "[feat/share 1a2b3c4d] Share dialog\n 3 files changed"),
		call(3, "m2", "claude-sonnet-5-5", 1, 10, 0, 0, 5, bash("t2", "git push -u origin feat/share && gh pr create --base dev --fill")),
		resultLine(4, "t2", "https://github.com/o/skipli/pull/28"),
		call(5, "m3", "claude-sonnet-5-5", 1, 10, 0, 0, 5, bash("t3", "git log --oneline -1")),
		resultLine(6, "t3", "[main 9999999] not a commit made here"),
		call(7, "m4", "claude-sonnet-5-5", 1, 10, 0, 0, 5, bash("t4", "git commit --amend --no-edit")),
	), skipliPlace)
	check(t, "commits and pull requests shipped", rows[0].Ships, `["commit 1a2b3c4","pr 28"]`)
	// A result read in the next pass still lands on the turn.
	_, rows = FoldTranscript(c, TranscriptFile{Session: "s"}, lines(resultLine(8, "t4", "[feat/share (root-commit) 5e6f7a8] Share")), skipliPlace)
	check(t, "result in a later pass", []any{rows[0].ID, rows[0].Ships}, `["s:1",["commit 5e6f7a8"]]`)
}
