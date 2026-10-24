package claude

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"berthier/internal/core"
)

func check(t *testing.T, name string, got any, want string) {
	t.Helper()
	var w, g any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("%s: bad want %q: %v", name, want, err)
	}
	b, _ := json.Marshal(got)
	json.Unmarshal(b, &g)
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s\n  got:  %s\n  want: %s", name, b, want)
	}
}

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

func TestFoldTranscriptTurns(t *testing.T) {
	c, rows := FoldTranscript(core.Cursor{}, core.TranscriptFile{Session: "s1"}, lines(
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
	c, rows = FoldTranscript(c, core.TranscriptFile{Session: "s1"}, lines(
		call(205, "m4", "claude-opus-5-5", 2, 1090, 0, 10, 5, ``),
	), skipliPlace)
	check(t, "continued turn", []any{rows[0].Key(), rows[0].Calls}, `["s1:2|claude-opus-5-5",1]`)
	merged := core.Turn{ID: "s1:2", Model: "claude-opus-5-5", Calls: 1, Start: 201, End: 201}
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
		check(t, tc.prompt, core.OriginOf(tc.prompt), `"`+tc.origin+`"`)
	}
	// Meta lines and local commands do not start turns; a subagent's turns are "subagent".
	_, rows := FoldTranscript(core.Cursor{}, core.TranscriptFile{Session: "p/agent-1", Parent: "p"}, lines(
		userLine(1, "look around"),
		call(2, "m1", "claude-haiku-5-5", 5, 0, 10, 0, 5, ``),
	), skipliPlace)
	check(t, "subagent", []any{rows[0].ID, rows[0].Parent, rows[0].Origin, rows[0].Prompt}, `["p/agent-1:1","p","subagent","look around"]`)
	_, rows = FoldTranscript(core.Cursor{}, core.TranscriptFile{Session: "s"}, lines(
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
	big := strings.Repeat("x", core.BigResult+4000)
	_, rows := FoldTranscript(core.Cursor{}, core.TranscriptFile{Session: "s"}, lines(
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

func TestFoldTranscriptShips(t *testing.T) {
	bash := func(id, cmd string) string {
		return fmt.Sprintf(`{"type":"tool_use","id":%q,"name":"Bash","input":{"command":%q}}`, id, cmd)
	}
	c, rows := FoldTranscript(core.Cursor{}, core.TranscriptFile{Session: "s"}, lines(
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
	_, rows = FoldTranscript(c, core.TranscriptFile{Session: "s"}, lines(resultLine(8, "t4", "[feat/share (root-commit) 5e6f7a8] Share")), skipliPlace)
	check(t, "result in a later pass", []any{rows[0].ID, rows[0].Ships}, `["s:1",["commit 5e6f7a8"]]`)
}

func TestCacheOf(t *testing.T) {
	lines := []string{
		`{"type":"user","timestamp":"2026-10-23T10:00:00Z","message":{"content":"go"}}`,
		`{"type":"assistant","timestamp":"2026-10-23T10:00:05Z","message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":3,"cache_read_input_tokens":150000,"cache_creation_input_tokens":2000,"output_tokens":500,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":2000}}}}`,
		`{"type":"assistant","timestamp":"2026-10-23T12:00:00Z","message":{"id":"m2","model":"<synthetic>","usage":{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":0}}}`,
		`{"type":"system","timestamp":"2026-10-23T12:00:01Z"}`,
	}
	c, ok := CacheOf(lines)
	check(t, "the last real call, not the synthetic limit stop", []any{ok, c}, `[true,{"last_at":1792749605,"context":152503,"model":"claude-opus-5-5","ttl_1h":true}]`)
	if _, ok := CacheOf(lines[2:]); ok {
		t.Error("a cache from no real call")
	}
}

func TestTranscriptInterrupted(t *testing.T) {
	sys := `{"type":"system","subtype":"turn_duration"}`
	asst := func(stop string) string {
		return `{"type":"assistant","message":{"stop_reason":"` + stop + `","content":[]}}`
	}
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"tool result last", []string{asst("tool_use"), `{"type":"user","message":{"role":"user","content":[{"type":"tool_result"}]}}`, sys}, true},
		{"task notification last", []string{asst("end_turn"), `{"type":"user","message":{"role":"user","content":"<task-notification>done</task-notification>"}}`}, true},
		{"unanswered tool call", []string{asst("tool_use")}, true},
		{"finished turn", []string{`{"type":"user","message":{"role":"user","content":"hi"}}`, asst("end_turn"), sys}, false},
		{"user interrupt", []string{`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}`}, false},
		{"no transcript", nil, false},
		{"torn line is skipped", []string{asst("tool_use"), `{"type":"us`}, true},
	}
	for _, c := range cases {
		if got := TranscriptInterrupted(c.lines); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}
