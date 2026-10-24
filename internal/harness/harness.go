// Package harness is Berthier's port to the Harness, the coding agent that
// runs in a pane (ADR 0006). Claude Code is today's adapter (internal/claude);
// another harness is a new adapter that passes the same contract tests
// (claudetest.Contract).
//
// The Harness owns everything specific to the agent program: how it is
// launched and cleared, what its keys mean, how to read its screen, what its
// hooks and transcripts say. It never touches the Host: it returns the
// physical keys and argv, and Berthier sends them. The doc comments on
// Harness are the written contract.
package harness

import (
	"time"

	"berthier/internal/core"
	"berthier/internal/host"
)

// Intent is what Berthier wants a key press to do to the agent.
type Intent string

const (
	// Interrupt stops the agent's current turn, leaving the session alive.
	Interrupt Intent = "interrupt"
	// Submit sends the input box's text.
	Submit Intent = "submit"
	// ClearDraft empties the input box without sending it.
	ClearDraft Intent = "clear-draft"
	// End quits the agent program, back to the shell. The keys may need a
	// moment between them.
	End Intent = "end"
)

// Intents are all of them, for commands to validate against.
var Intents = []Intent{Interrupt, Submit, ClearDraft, End}

// State is what a hook says the agent is doing.
type State string

const (
	// Started: the session began (or resumed, or was cleared) and waits for input.
	Started State = "started"
	Working State = "working"
	// Idle: the turn ended.
	Idle State = "idle"
	// Blocked: it waits for the user (a permission prompt, a question).
	Blocked State = "blocked"
	// Exited: the session ended; Reason says how.
	Exited State = "exited"
)

// HookEvent is a hook payload, read.
type HookEvent struct {
	Session    string
	Event      string // the harness's own name for it
	State      State  // "" when the event does not change the agent's state
	Reason     string // why a session ended, for Exited
	Cwd        string
	Transcript string // the session's transcript file, when the payload names it
}

// LaunchOpts says how a session starts.
type LaunchOpts struct {
	Resume   string   // a session to resume; empty starts a new one
	Model    string   // empty: the harness's default
	Disallow []string // tools the agent may not use
}

// Placer says which Corps and Division a working directory belongs to; ok is
// false for none.
type Placer func(cwd string) (corps, division string, ok bool)

// Harness is the agent program. Methods are pure functions of their arguments
// and the adapter's configuration, safe to call from any goroutine; the ones
// that read files say so.
type Harness interface {
	// Launch is the argv that starts a session in a pane at a shell. A
	// resumed session keeps its conversation; a new one starts empty.
	Launch(o LaunchOpts) []string
	// ClearCommand is the text, sent as a prompt, that drops the session's
	// context and starts a new session in the same process (Claude: /clear).
	ClearCommand() string
	// Keys is the physical key sequence for an intent.
	Keys(i Intent) []host.Key

	// Draft: does the input box in screen (read with ANSI) hold text someone
	// typed? The agent's own suggestion is not a draft.
	Draft(screen string) bool
	// PromptHeld: does the input box in screen still hold text, taken from
	// the last text sent, unsent (a paste not yet submitted)?
	PromptHeld(screen, text string) bool
	// UsageLimit: is the agent stopped on its usage limit, as the last thing
	// above its input box in screen, and when does the limit reset (unix
	// seconds)? since is when the stop was first seen; local is the zone used
	// when the screen names none.
	UsageLimit(screen string, since int64, local *time.Location) (reset int64, ok bool)
	// ExitedDeliberately: does tail, the end of the pane's text after the
	// agent vanished, show a clean exit of session rather than a crash?
	ExitedDeliberately(tail, session string) bool

	// Hook reads the payload of a hook event (stdin of `berthier hook`).
	// Events it does not know, or payloads it cannot read, are errors; events
	// that do not change the agent's state return State "".
	Hook(event string, payload []byte) (HookEvent, error)

	// Transcripts are the files of a session, newest layout first (reads the
	// disk); none when the session was never saved and cannot be resumed.
	Transcripts(session string) []string
	// AllTranscripts are every transcript under the projects matching glob
	// ("*" for all): main sessions and subagents (reads the disk).
	AllTranscripts(projects string) []string
	// TranscriptFile names a transcript file for Spend: a main session, or a
	// subagent of a parent.
	TranscriptFile(path string) core.TranscriptFile
	// Spend folds the complete lines appended to a transcript since its
	// cursor into one row per turn and model (ADR 0005).
	Spend(c core.Cursor, f core.TranscriptFile, lines [][]byte, place Placer) (core.Cursor, []core.Turn)
	// Interrupted: does the end of a transcript (its last lines) stop mid-turn,
	// not on a finished turn or the user's own interrupt?
	Interrupted(lines []string) bool
	// CacheOf reads the end of a transcript for the session's last real call.
	CacheOf(lines []string) (core.Cache, bool)
}
