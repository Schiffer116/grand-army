// Package herdr talks to the Herdr CLI: commands against the Grand Army's
// session, and the prompt that makes sure Claude took what it was sent.
package herdr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"berthier/internal/claude"
	"berthier/internal/harness"
)

// Client runs `herdr`. Session empty means the user's default Herdr session;
// tests point it at an isolated named one so they never touch live workspaces.
type Client struct {
	Bin        string
	Session    string
	PromptPoll time.Duration // how often PromptAgent checks that Claude took a prompt
	// Harness reads the Agent's screen; the zero value is Claude Code. (Until
	// the Host port replaces Client, which then takes the Harness as a port.)
	Harness harness.Harness
}

func (c Client) harness() harness.Harness {
	if c.Harness == nil {
		return claude.Claude{}
	}
	return c.Harness
}

// FromEnv is the client the BERTHIER_HERDR* variables describe.
func FromEnv(getenv func(string) string, poll time.Duration) Client {
	c := Client{Bin: getenv("BERTHIER_HERDR"), Session: getenv("BERTHIER_HERDR_SESSION"), PromptPoll: poll}
	if c.Bin == "" {
		c.Bin = "herdr"
	}
	return c
}

// Command builds a `herdr` command against the session. Inherited pane
// context would otherwise pin us to whichever session spawned this process.
func (c Client) Command(args ...string) *exec.Cmd {
	if c.Session == "" {
		return exec.Command(c.Bin, args...)
	}
	cmd := exec.Command(c.Bin, append([]string{"--session", c.Session}, args...)...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HERDR_SOCKET_PATH=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	return cmd
}

// Run runs a Herdr command and returns its stdout; a failure carries Herdr's
// own message when it gave one.
func (c Client) Run(args ...string) ([]byte, error) {
	var out, errb bytes.Buffer
	cmd := c.Command(args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return out.Bytes(), err
		}
		return out.Bytes(), errorFrom(errb.Bytes(), err)
	}
	return out.Bytes(), nil
}

// errorFrom is Herdr's message from its JSON error output.
func errorFrom(stderr []byte, fallback error) error {
	var e struct{ Error struct{ Message string } }
	if json.Unmarshal(stderr, &e) == nil && e.Error.Message != "" {
		return errors.New(e.Error.Message)
	}
	if s := strings.TrimSpace(string(stderr)); s != "" {
		return errors.New(s)
	}
	return fallback
}

// JSON runs a Herdr command and decodes its JSON output into v.
func (c Client) JSON(v any, args ...string) error {
	out, err := c.Run(args...)
	if err != nil {
		return err
	}
	return json.Unmarshal(out, v)
}

type agentInfo struct {
	Seq     json.Number
	Status  string
	Session string
}

func (c Client) agentState(pane string) (agentInfo, error) {
	var got struct {
		Result struct {
			Agent struct {
				Seq     json.Number `json:"state_change_seq"`
				Status  string      `json:"agent_status"`
				Session struct {
					Value string `json:"value"`
				} `json:"agent_session"`
			}
		}
	}
	out, err := c.Command("agent", "get", pane).Output()
	if err != nil || json.Unmarshal(out, &got) != nil || got.Result.Agent.Status == "" {
		return agentInfo{}, fmt.Errorf("no agent in %s", pane)
	}
	return agentInfo{got.Result.Agent.Seq, got.Result.Agent.Status, got.Result.Agent.Session.Value}, nil
}

// ErrDraft is a prompt not sent because the Agent's input box holds text
// someone typed there (the user may be mid-dictation).
var ErrDraft = errors.New("its input box holds the user's draft (they may be typing); not sent, try again later")

// HasDraft reports whether the Claude input box in pane holds text someone
// typed (Harness.Draft); Claude's dim suggested prompt is not a draft.
func (c Client) HasDraft(pane string) (bool, error) {
	screen, err := c.Run("pane", "read", pane, "--source", "visible", "--format", "ansi")
	if err != nil {
		return false, err
	}
	return c.harness().Draft(string(screen)), nil
}

// PromptAgent prompts the Agent in pane and makes sure Claude took it: `agent
// prompt` only types the text and Enter, and Claude swallows that Enter while
// it turns pasted image paths into attachments, leaving a draft. Taken once
// the Agent changes state, or, busy, its box is empty (queued). A draft gets
// Enter, a lost paste is sent again. The error says why (Herdr's message).
// It never types into a box holding the user's draft: ErrDraft.
func (c Client) PromptAgent(pane, text string) error {
	before, err := c.agentState(pane)
	if err != nil {
		return err
	}
	if d, _ := c.HasDraft(pane); d {
		return fmt.Errorf("%s: %w", pane, ErrDraft)
	}
	if _, err := c.Run("agent", "prompt", pane, text); err != nil {
		return err
	}
	tries := 0
	for polls := 1; ; polls++ {
		time.Sleep(c.PromptPoll)
		now, err := c.agentState(pane)
		if err != nil {
			return fmt.Errorf("agent in %s went away", pane)
		}
		if now.Seq != before.Seq {
			return nil
		}
		if polls%4 != 0 {
			continue
		}
		screen, _ := c.Command("pane", "read", pane, "--source", "visible").Output()
		holds := c.harness().PromptHeld(string(screen), text)
		if !holds && now.Status == "working" {
			return nil
		}
		if tries++; tries > 3 {
			return fmt.Errorf("Claude in %s did not take the prompt", pane)
		}
		if holds {
			c.Run("agent", "send-keys", pane, "enter")
		} else {
			c.Run("agent", "prompt", pane, text)
		}
	}
}

// ClearAgent runs Claude's local `/clear` in pane and returns once the Agent
// is in a new session (its context is gone), the proof it took; a stuck draft
// or an unchanged session is an error. Like PromptAgent it never types into a
// box holding the user's draft.
func (c Client) ClearAgent(pane string) error {
	before, err := c.agentState(pane)
	if err != nil {
		return err
	}
	if d, _ := c.HasDraft(pane); d {
		return fmt.Errorf("%s: %w", pane, ErrDraft)
	}
	if _, err := c.Run("agent", "prompt", pane, c.harness().ClearCommand()); err != nil {
		return err
	}
	for polls := 1; polls <= 40; polls++ {
		time.Sleep(c.PromptPoll)
		now, err := c.agentState(pane)
		if err != nil {
			return fmt.Errorf("agent in %s went away", pane)
		}
		if now.Session != "" && now.Session != before.Session {
			return nil
		}
		// Claude's slash menu can hold the command until Enter.
		if polls%8 == 0 {
			if screen, _ := c.Command("pane", "read", pane, "--source", "visible").Output(); c.harness().PromptHeld(string(screen), "/clear") {
				c.Run("agent", "send-keys", pane, "enter")
			}
		}
	}
	return fmt.Errorf("Claude in %s did not clear its context", pane)
}
