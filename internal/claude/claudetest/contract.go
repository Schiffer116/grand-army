package claudetest

import (
	"slices"
	"testing"
	"time"

	"berthier/internal/core"
	"berthier/internal/harness"
	"berthier/internal/host"
)

// HookCase is one hook the agent fires: its event name and payload.
type HookCase struct {
	Event   string
	Payload string
}

// Subject is a Harness and the samples of its own agent the contract reads it
// with: screens and transcripts in the agent's real format.
type Subject struct {
	H harness.Harness

	// Screens. Draft and Suggestion are read with ANSI, as the Host gives them.
	Draft, Suggestion, Empty string
	Held, Sent               string // the box still holds HeldText / HeldText was taken
	HeldText                 string
	Limit, Idle              string // stopped on a usage limit / not
	LimitSince, LimitReset   int64

	// Hooks.
	Submit, Stop, Permission, Question, Note, End, Start HookCase
	Reason                                               string // the End hook's reason: a deliberate exit
	OtherReason                                          string // a reason that is not
	HookSession                                          string // the session all hook payloads name

	// Transcripts. Write saves lines as session's transcript where H finds it
	// and returns its path.
	Write               func(t *testing.T, session string, lines []string) string
	Finished, MidTurn   []string
	Call                []string // ends on a real API call
	CallAt, CallContext int64
	CallModel           string
	Chat                []string // a conversation: a user message, then an assistant reply
	ChatItems           int      // how many items it makes
	ChatLast            string   // the text of the assistant's reply
	// Skill makes one skill available to an agent in the cwd it returns, and names it.
	Skill             func(t *testing.T) (cwd, name string)
	Spend             [][]byte // one turn of two calls
	SpendIn, SpendOut int64    // their summed tokens
	SpendCalls        int
	Place             harness.Placer
}

// Contract is the behaviour every Harness adapter and the Fake share.
func Contract(t *testing.T, s Subject) {
	h := s.H
	t.Run("launch", func(t *testing.T) {
		plain := h.Launch(harness.LaunchOpts{})
		if len(plain) == 0 || plain[0] == "" {
			t.Fatalf("Launch = %v, want a program", plain)
		}
		full := h.Launch(harness.LaunchOpts{Resume: "sess-1", Model: "model-x", Disallow: []string{"ToolY"}})
		for _, want := range []string{"sess-1", "model-x", "ToolY"} {
			if !slices.Contains(full, want) {
				t.Errorf("Launch with options = %v, lacks %q", full, want)
			}
		}
		if full[0] != plain[0] {
			t.Errorf("the program changed with options: %v vs %v", full, plain)
		}
		if slices.Contains(plain, "sess-1") || slices.Contains(plain, "model-x") {
			t.Errorf("Launch without options names a session or model: %v", plain)
		}
		if h.ClearCommand() == "" {
			t.Error("ClearCommand is empty")
		}
	})

	t.Run("keys", func(t *testing.T) {
		for _, i := range harness.Intents {
			ks := h.Keys(i)
			if len(ks) == 0 {
				t.Errorf("Keys(%s) is empty", i)
			}
			for _, k := range ks {
				if !slices.Contains(host.Keys, k) {
					t.Errorf("Keys(%s) has %q, not a physical key of the Host", i, k)
				}
			}
		}
		if h.Keys("nope") != nil {
			t.Error("Keys of an unknown intent")
		}
		// An interrupt must leave the session alive: it is never what ends it.
		if slices.Equal(h.Keys(harness.Interrupt), h.Keys(harness.End)) {
			t.Error("Interrupt and End are the same keys")
		}
	})

	t.Run("screens", func(t *testing.T) {
		for name, c := range map[string]struct {
			got, want bool
		}{
			"draft is a draft":                 {h.Draft(s.Draft), true},
			"a suggestion is not a draft":      {h.Draft(s.Suggestion), false},
			"an empty box is not a draft":      {h.Draft(s.Empty), false},
			"the text held in the box":         {h.PromptHeld(s.Held, s.HeldText), true},
			"the text taken":                   {h.PromptHeld(s.Sent, s.HeldText), false},
			"a suggestion is not our text":     {h.PromptHeld(s.Suggestion, s.HeldText), false},
			"an empty box holds nothing":       {h.PromptHeld(s.Empty, s.HeldText), false},
			"the user's own exit":              {h.DeliberateExit(s.Reason), true},
			"another reason is not deliberate": {h.DeliberateExit(s.OtherReason), false},
			"no reason is not deliberate":      {h.DeliberateExit(""), false},
		} {
			if c.got != c.want {
				t.Errorf("%s: got %v", name, c.got)
			}
		}
		loc := time.UTC
		if r, ok := h.UsageLimit(s.Limit, s.LimitSince, loc); !ok || r != s.LimitReset {
			t.Errorf("UsageLimit of a limit stop = %d, %v; want %d", r, ok, s.LimitReset)
		}
		if _, ok := h.UsageLimit(s.Idle, s.LimitSince, loc); ok {
			t.Error("UsageLimit of an idle screen")
		}
	})

	t.Run("hooks", func(t *testing.T) {
		for name, c := range map[string]struct {
			hook  HookCase
			state harness.State
		}{
			"prompt submitted": {s.Submit, harness.Working},
			"turn stopped":     {s.Stop, harness.Idle},
			"permission":       {s.Permission, harness.Blocked},
			"question":         {s.Question, harness.Blocked},
			"other notice":     {s.Note, ""},
			"session ended":    {s.End, harness.Exited},
			"session started":  {s.Start, harness.Started},
		} {
			got, err := h.Hook(c.hook.Event, []byte(c.hook.Payload))
			if err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			if got.State != c.state || got.Session != s.HookSession || got.Event != c.hook.Event {
				t.Errorf("%s: %+v, want state %q of session %q", name, got, c.state, s.HookSession)
			}
			if c.state == harness.Exited && got.Reason != s.Reason {
				t.Errorf("%s: reason %q, want %q", name, got.Reason, s.Reason)
			}
		}
		if _, err := h.Hook("no-such-event", []byte(`{}`)); err == nil {
			t.Error("an unknown event is not an error")
		}
		if _, err := h.Hook(s.Stop.Event, []byte(`not json`)); err == nil {
			t.Error("an unreadable payload is not an error")
		}
	})

	t.Run("transcripts", func(t *testing.T) {
		if got := h.Transcripts("never-saved"); len(got) != 0 {
			t.Errorf("Transcripts of an unsaved session = %v", got)
		}
		path := s.Write(t, "saved-1", s.Finished)
		if got := h.Transcripts("saved-1"); !slices.Equal(got, []string{path}) {
			t.Errorf("Transcripts = %v, want [%s]", got, path)
		}
		if got := h.AllTranscripts("*"); !slices.Contains(got, path) {
			t.Errorf("AllTranscripts = %v, lacks %s", got, path)
		}
		if f := h.TranscriptFile(path); f.Session != "saved-1" || f.Parent != "" {
			t.Errorf("TranscriptFile = %+v", f)
		}
		if h.Interrupted(s.Finished) {
			t.Error("a finished turn is interrupted")
		}
		if !h.Interrupted(s.MidTurn) {
			t.Error("a transcript cut mid-turn is not interrupted")
		}
		if h.Interrupted(nil) {
			t.Error("no transcript is interrupted")
		}
		c, ok := h.CacheOf(s.Call)
		if !ok || c.LastAt != s.CallAt || c.Context != s.CallContext || c.Model != s.CallModel {
			t.Errorf("CacheOf = %+v, %v; want at %d, context %d, %s", c, ok, s.CallAt, s.CallContext, s.CallModel)
		}
		if _, ok := h.CacheOf(s.Finished); ok {
			t.Error("a cache from no call")
		}
	})

	t.Run("spend", func(t *testing.T) {
		f := core.TranscriptFile{Session: "spent-1"}
		cur, rows := h.Spend(core.Cursor{}, f, s.Spend, s.Place)
		if len(rows) != 1 {
			t.Fatalf("Spend rows = %+v, want one turn", rows)
		}
		r := rows[0]
		if r.Session != "spent-1" || r.Calls != s.SpendCalls || r.In != s.SpendIn || r.Out != s.SpendOut {
			t.Errorf("turn = %+v, want %d calls, %d in, %d out", r, s.SpendCalls, s.SpendIn, s.SpendOut)
		}
		// Nothing new, nothing more to count: the cursor carries what it needs.
		if _, again := h.Spend(cur, f, nil, s.Place); len(again) != 0 {
			t.Errorf("Spend of no new lines made rows: %+v", again)
		}
	})

	t.Run("chat", func(t *testing.T) {
		if _, ok := h.Chat("never-saved"); ok {
			t.Error("a chat of an unsaved session")
		}
		s.Write(t, "chatty-1", s.Chat)
		c, ok := h.Chat("chatty-1")
		if !ok || len(c.Items) != s.ChatItems || c.Session != "chatty-1" {
			t.Fatalf("Chat = %+v, %v; want %d items of chatty-1", c, ok, s.ChatItems)
		}
		if last, ok := h.LastMessage("chatty-1"); !ok || last.Text != s.ChatLast {
			t.Errorf("LastMessage = %+v, %v; want %q", last, ok, s.ChatLast)
		}
		if _, ok := h.LastMessage("never-saved"); ok {
			t.Error("a last message of an unsaved session")
		}
	})

	t.Run("skills", func(t *testing.T) {
		cwd, name := s.Skill(t)
		for _, sk := range h.Skills(cwd) {
			if sk.Name == name {
				return
			}
		}
		t.Errorf("Skills(%s) lacks %q", cwd, name)
	})
}
