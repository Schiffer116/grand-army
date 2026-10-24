// Package claudetest is the Go fake of the Harness port and the contract suite
// both it and the real adapter must pass. The fake reads small text formats of
// its own, so tests can script an Agent without Claude's screens:
//
//	screen   "box: <text>" is the input box holding text someone typed, "hint: <text>"
//	         its suggestion; "limit resets <unix>" a usage-limit stop above the box
//	tail     "exited <session>" is a clean exit
//	hooks    events submit, stop, permission, question, note, end, start;
//	         payload {"session": "...", "reason": "..."}
//	transcript lines  "cut" ends mid-turn; "call <unix> <model> <context>" is an API
//	         call; "turn <in> <out>" is one call of the session's turn
package claudetest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"berthier/internal/core"
	"berthier/internal/harness"
	"berthier/internal/host"
)

// Fake is an in-memory Harness. Dir is where its transcripts live; Chats and
// SkillList are what it answers Chat and Skills with.
type Fake struct {
	Dir       string
	Chats     map[string]harness.Chat
	SkillList []harness.Skill
}

var _ harness.Harness = Fake{}

// Box is a screen whose input box holds text; Hint one showing a suggestion.
func Box(text string) string  { return "history\nbox: " + text }
func Hint(text string) string { return "history\nhint: " + text }

// Limit is a screen stopped on a usage limit that resets at unix seconds.
func Limit(reset int64) string { return fmt.Sprintf("history\nlimit resets %d\nbox:", reset) }

func (Fake) Launch(o harness.LaunchOpts) []string {
	argv := []string{"fake-agent"}
	if o.Resume != "" {
		argv = append(argv, "--resume", o.Resume)
	}
	if o.Model != "" {
		argv = append(argv, "--model", o.Model)
	}
	for _, t := range o.Disallow {
		argv = append(argv, "--deny", t)
	}
	if o.Settings != "" {
		argv = append(argv, "--settings", o.Settings)
	}
	return argv
}

func (Fake) ClearCommand() string { return "/clear" }

func (Fake) Keys(i harness.Intent) []host.Key {
	switch i {
	case harness.Interrupt:
		return []host.Key{host.KeyEscape}
	case harness.Submit:
		return []host.Key{host.KeyEnter}
	case harness.ClearDraft:
		return []host.Key{host.KeyCtrlU}
	case harness.End:
		return []host.Key{host.KeyCtrlC, host.KeyCtrlC}
	}
	return nil
}

// boxText is what the input box of a screen holds, and whether it is typed.
func boxText(screen string) (text string, typed bool) {
	for _, l := range strings.Split(screen, "\n") {
		if t, ok := strings.CutPrefix(l, "box:"); ok {
			text, typed = strings.TrimSpace(t), true
		} else if t, ok := strings.CutPrefix(l, "hint:"); ok {
			text, typed = strings.TrimSpace(t), false
		}
	}
	return text, typed
}

func (Fake) Draft(screen string) bool {
	text, typed := boxText(screen)
	return typed && text != ""
}

func (Fake) PromptHeld(screen, text string) bool {
	held, typed := boxText(screen)
	key := strings.Join(strings.Fields(text), "")
	held = strings.Join(strings.Fields(held), "")
	return typed && held != "" && key != "" && strings.Contains(held, key[:min(len(key), 24)])
}

func (Fake) UsageLimit(screen string, since int64, local *time.Location) (int64, bool) {
	for _, l := range strings.Split(screen, "\n") {
		if t, ok := strings.CutPrefix(l, "limit resets "); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil {
				return n, true
			}
			return since + 18000, true
		}
	}
	return 0, false
}

// DeliberateExit: the reason "exit".
func (Fake) DeliberateExit(reason string) bool { return reason == "exit" }

func (Fake) Hook(event string, payload []byte) (harness.HookEvent, error) {
	var in struct{ Session, Reason, Cwd string }
	if err := json.Unmarshal(payload, &in); err != nil {
		return harness.HookEvent{}, fmt.Errorf("%s hook payload: %w", event, err)
	}
	h := harness.HookEvent{Session: in.Session, Event: event, Cwd: in.Cwd}
	switch event {
	case "submit":
		h.State = harness.Working
	case "stop":
		h.State = harness.Idle
	case "permission", "question":
		h.State = harness.Blocked
	case "end":
		h.State, h.Reason = harness.Exited, in.Reason
	case "start":
		h.State = harness.Started
	case "note":
	default:
		return harness.HookEvent{}, fmt.Errorf("unknown hook event %q", event)
	}
	return h, nil
}

// Path is where the Fake keeps a session's transcript.
func (f Fake) Path(session string) string {
	return filepath.Join(f.Dir, "projects", "p", session+".jsonl")
}

func (f Fake) Transcripts(session string) []string {
	if _, err := os.Stat(f.Path(session)); err != nil {
		return nil
	}
	return []string{f.Path(session)}
}

func (f Fake) AllTranscripts(projects string) []string {
	m, _ := filepath.Glob(filepath.Join(f.Dir, "projects", projects, "*.jsonl"))
	return m
}

func (Fake) TranscriptFile(path string) core.TranscriptFile {
	return core.TranscriptFile{Session: strings.TrimSuffix(filepath.Base(path), ".jsonl")}
}

func (Fake) Spend(c core.Cursor, f core.TranscriptFile, lines [][]byte, place harness.Placer) (core.Cursor, []core.Turn) {
	c.Session, c.Seq = f.Session, 1
	var t *core.Turn
	for _, l := range lines {
		var in, out int64
		if n, _ := fmt.Sscanf(string(l), "turn %d %d", &in, &out); n != 2 {
			continue
		}
		if t == nil {
			t = &core.Turn{ID: f.Session + ":1", Session: f.Session, Model: "fake", Origin: "user"}
			t.Corps, t.Division, _ = place("")
		}
		t.Calls++
		t.Tokens.Add(core.Tokens{In: in, Out: out})
	}
	if t == nil {
		return c, nil
	}
	return c, []core.Turn{*t}
}

func lastLine(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}

func (Fake) Interrupted(lines []string) bool { return lastLine(lines) == "cut" }

func (Fake) CacheOf(lines []string) (core.Cache, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		var at, ctx int64
		var model string
		if n, _ := fmt.Sscanf(lines[i], "call %d %s %d", &at, &model, &ctx); n == 3 {
			return core.Cache{LastAt: at, Context: ctx, Model: model, TTL1h: true}, true
		}
	}
	return core.Cache{}, false
}

func (f Fake) Chat(session string) (harness.Chat, bool) {
	c, ok := f.Chats[session]
	return c, ok
}

func (f Fake) LastMessage(session string) (harness.ChatItem, bool) {
	c, ok := f.Chats[session]
	for i := len(c.Items) - 1; ok && i >= 0; i-- {
		if c.Items[i].Role == "assistant" {
			return c.Items[i], true
		}
	}
	return harness.ChatItem{}, false
}

func (f Fake) Skills(cwd string) []harness.Skill { return f.SkillList }
