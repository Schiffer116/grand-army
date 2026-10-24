package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func writeJSONL(t *testing.T, path string, lines ...string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		f.WriteString(l)
	}
}

func TestTranscriptReadsTheConversation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeJSONL(t, path,
		`{"type":"user","timestamp":"t1","message":{"content":"fix the bug"}}`+"\n",
		`{"type":"user","isMeta":true,"message":{"content":"meta"}}`+"\n",
		`{"type":"user","message":{"content":"<local-command-stdout>x</local-command-stdout>"}}`+"\n",
		`{"type":"user","message":{"content":"<command-name>/review</command-name><command-args>42</command-args>"}}`+"\n",
		`{"type":"assistant","isSidechain":true,"message":{"content":[{"type":"text","text":"subagent"}]}}`+"\n",
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"Looking."},{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"ls","description":"List"}}]}}`+"\n",
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","content":[{"type":"text","text":"a.go"}],"is_error":true}]}}`+"\n",
		`{"type":"attachment","attachment":{"type":"queued_command","prompt":"also this","commandMode":"prompt","timestamp":"t2"}}`+"\n",
		`{"type":"attachment","attachment":{"type":"queued_command","prompt":"<task-notification>","commandMode":"task-notification"}}`+"\n",
		`{"type":"assistant","message":{"content":[{"type":"text","text":"half`, // still being written
	)
	tr := NewTranscripts(t.TempDir())
	items, rev, err := tr.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []ChatItem{
		{Role: "user", Text: "fix the bug", At: "t1"},
		{Role: "user", Text: "/review 42"},
		{Role: "assistant", Text: "Looking."},
		{Role: "tool", Tool: "Bash", Input: "ls", Result: "a.go", Error: true},
		{Role: "user", Text: "also this", At: "t2"},
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("items\n got %+v\nwant %+v", items, want)
	}

	writeJSONL(t, path, ` done"}]}}`+"\n")
	items, rev2, _ := tr.Read(path)
	if rev2 <= rev || len(items) != 6 || items[5].Text != "half done" {
		t.Fatalf("after append: rev %d -> %d, items %+v", rev, rev2, items)
	}
}

func TestTranscriptPathFollowsTheLiveDescendantSession(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sessions"), 0o755)
	os.MkdirAll(filepath.Join(dir, "projects", "-some-cwd"), 0o755)
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	session := func(pid int, id, kind string, at int64) {
		b, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": id, "kind": kind, "updatedAt": at})
		os.WriteFile(filepath.Join(dir, "sessions", fmt.Sprint(pid)+".json"), b, 0o644)
		os.WriteFile(filepath.Join(dir, "projects", "-some-cwd", id+".jsonl"), nil, 0o644)
	}
	tr := NewTranscripts(dir)
	session(os.Getpid(), "pane", "interactive", 1)
	session(1, "unrelated", "bg", 99)
	session(child.Process.Pid, "nested", "print", 3)
	if got := tr.Path("pane"); filepath.Base(got) != "pane.jsonl" {
		t.Fatalf("no forked descendant: %q", got)
	}
	session(child.Process.Pid, "fork", "bg", 2)
	if got := tr.Path("pane"); filepath.Base(got) != "fork.jsonl" {
		t.Fatalf("with a forked session: %q", got)
	}
	if got := tr.Path("missing"); got != "" {
		t.Fatalf("unknown session: %q", got)
	}
}

func TestClaudeChat(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "projects", "-cwd"), 0o755)
	writeJSONL(t, filepath.Join(dir, "projects", "-cwd", "s1.jsonl"), `{"type":"user","message":{"content":"hi"}}`+"\n")
	c := Claude{ConfigDir: dir}
	chat, ok := c.Chat("s1")
	if !ok || chat.Session != "s1" || len(chat.Items) != 1 || chat.Items[0].Text != "hi" {
		t.Fatalf("Chat = %+v, %v", chat, ok)
	}
	writeJSONL(t, filepath.Join(dir, "projects", "-cwd", "s1.jsonl"), `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`+"\n")
	next, _ := c.Chat("s1")
	if len(next.Items) != 2 || next.Rev == chat.Rev {
		t.Errorf("after an append: %+v", next)
	}
	if last, ok := c.LastMessage("s1"); !ok || last.Text != "hello" {
		t.Errorf("LastMessage = %+v, %v", last, ok)
	}
	if _, ok := c.Chat("nope"); ok {
		t.Error("a chat of no session")
	}
}

func TestTranscriptInfoFollowsTheLatestAssistantMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeJSONL(t, path,
		`{"type":"assistant","gitBranch":"old","cwd":"/a","effort":"low","message":{"model":"claude-sonnet-5-5","usage":{"input_tokens":1,"cache_read_input_tokens":1000}}}`+"\n",
		`{"type":"assistant","gitBranch":"feat","cwd":"/b","effort":"high","message":{"model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_read_input_tokens":90000,"cache_creation_input_tokens":8}}}`+"\n",
		`{"type":"assistant","message":{"model":"<synthetic>","usage":{}}}`+"\n",
	)
	tr := NewTranscripts(t.TempDir())
	if _, _, err := tr.Read(path); err != nil {
		t.Fatal(err)
	}
	want := AgentInfo{Model: "Opus 5.5", ModelID: "claude-opus-5-5", Effort: "high", ContextTokens: 90010, ContextPct: 45, Branch: "feat", Cwd: "/b"}
	if got := tr.Info(path); got != want {
		t.Fatalf("info = %+v, want %+v", got, want)
	}
}

func TestFriendlyModel(t *testing.T) {
	for id, want := range map[string]string{
		"claude-opus-5-5": "Opus 5.5", "claude-haiku-4-5-20251001": "Haiku 4.5",
		"claude-fable-5-1": "Fable 5.1", "gpt-9": "gpt-9", "claude-x": "claude-x",
	} {
		if got := friendlyModel(id); got != want {
			t.Errorf("friendlyModel(%q) = %q, want %q", id, got, want)
		}
	}
}
