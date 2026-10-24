package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"berthier/internal/claude/claudetest"
)

// TestContract runs the Harness contract against Claude Code, with its real
// screens, hook payloads and transcripts.
func TestContract(t *testing.T) {
	dir := t.TempDir()
	rule := "\x1b[0m" + strings.Repeat("─", 50) + "\x1b[0m\r"
	box := func(l string) string { return "● pong\r\n\r\n" + rule + "\n" + l + "\r\n" + rule + "\n  5h: 22%" }
	const held = "Reply with just: pong, then stop"
	hook := func(event, extra string) claudetest.HookCase {
		return claudetest.HookCase{Event: event, Payload: `{"session_id":"s1","cwd":"/r","transcript_path":"/t/s1.jsonl","hook_event_name":"` + event + `"` + extra + `}`}
	}
	limitBox := "──────────────────────────────────────────────────\n❯\n──────────────────────────────────────────────────"
	claudetest.Contract(t, claudetest.Subject{
		H:          Claude{ConfigDir: dir},
		Draft:      box("❯ hello I am dictating"),
		Suggestion: box("❯ \x1b[2mpush it\x1b[0m"),
		Empty:      box("❯ "),
		HeldText:   held,
		Held:       sgr.ReplaceAllString(box("❯ Reply with just:\n  pong, then stop"), ""),
		Sent:       sgr.ReplaceAllString(box("❯ "), ""),
		Limit:      "  ⎿  You've hit your session limit · resets 11pm (UTC)\n● Usage limit reached · continuing automatically at 11pm · esc to cancel\n" + limitBox,
		Idle:       "● Pushed PR #18.\n" + limitBox,
		// 2026-10-20 21:30 UTC, and the 11pm that follows.
		LimitSince: 1792531800, LimitReset: 1792537200,

		Submit:      hook("UserPromptSubmit", `,"prompt":"hi"`),
		Stop:        hook("Stop", `,"stop_hook_active":false`),
		Permission:  hook("Notification", `,"notification_type":"permission_prompt","message":"Claude needs your permission to use Bash"`),
		Question:    hook("PreToolUse", `,"tool_name":"AskUserQuestion","tool_input":{}`),
		Note:        hook("Notification", `,"notification_type":"idle_prompt","message":"Claude is waiting for your input"`),
		End:         hook("SessionEnd", `,"reason":"prompt_input_exit"`),
		Start:       hook("SessionStart", `,"source":"startup"`),
		Reason:      "prompt_input_exit",
		OtherReason: "other",
		HookSession: "s1",

		Write: func(t *testing.T, session string, lines []string) string {
			p := filepath.Join(dir, "projects", "-r", session+".jsonl")
			os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return p
		},
		Finished: []string{userLine(0, "hi"), `{"type":"assistant","message":{"stop_reason":"end_turn","content":[]}}`},
		MidTurn:  []string{userLine(0, "hi"), `{"type":"assistant","message":{"stop_reason":"tool_use","content":[]}}`},
		Call: []string{userLine(0, "hi"),
			call(5, "m1", "claude-opus-5-5", 3, 150000, 0, 2000, 500, ""),
		},
		CallAt: 5, CallContext: 152503, CallModel: "claude-opus-5-5",
		Chat: []string{`{"type":"user","message":{"content":"hi"}}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`},
		ChatItems: 2, ChatLast: "hello",
		Skill: func(t *testing.T) (string, string) {
			cwd := t.TempDir()
			put(t, cwd+"/.claude/skills/deploy/SKILL.md", "---\nname: deploy\ndescription: ship it\n---\n")
			return cwd, "deploy"
		},
		Spend: lines(
			userLine(0, "go"),
			call(1, "m1", "claude-sonnet-5-5", 10, 0, 100, 0, 2, ""),
			call(2, "m2", "claude-sonnet-5-5", 5, 100, 0, 0, 3, ""),
		),
		SpendIn: 15, SpendOut: 5, SpendCalls: 2,
		Place: func(string) (string, string, bool) { return "skipli", "", true },
	})
}
