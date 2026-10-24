package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	"berthier/internal/harness"
)

// Hook reads the payload of a Claude Code hook event. State follows the
// design of ADR 0006: UserPromptSubmit is working, Stop idle, a permission
// prompt (Notification) or an AskUserQuestion about to run (PreToolUse)
// blocked, SessionEnd exited with its reason, SessionStart started. Other
// notifications and tools do not change the state.
func (Claude) Hook(event string, payload []byte) (harness.HookEvent, error) {
	var in struct {
		SessionID        string `json:"session_id"`
		TranscriptPath   string `json:"transcript_path"`
		Cwd              string `json:"cwd"`
		ToolName         string `json:"tool_name"`
		Reason           string `json:"reason"`
		Message          string `json:"message"`
		NotificationType string `json:"notification_type"`
	}
	if err := json.Unmarshal(payload, &in); err != nil {
		return harness.HookEvent{}, fmt.Errorf("%s hook payload: %w", event, err)
	}
	h := harness.HookEvent{Session: in.SessionID, Event: event, Cwd: in.Cwd, Transcript: in.TranscriptPath}
	switch event {
	case "UserPromptSubmit":
		h.State = harness.Working
	case "Stop":
		h.State = harness.Idle
	case "Notification":
		if in.NotificationType == "permission_prompt" || (in.NotificationType == "" && strings.Contains(strings.ToLower(in.Message), "permission")) {
			h.State = harness.Blocked
		}
	case "PreToolUse":
		if in.ToolName == "AskUserQuestion" {
			h.State = harness.Blocked
		}
	case "SessionEnd":
		h.State, h.Reason = harness.Exited, in.Reason
	case "SessionStart":
		h.State = harness.Started
	default:
		return harness.HookEvent{}, fmt.Errorf("unknown hook event %q", event)
	}
	return h, nil
}
