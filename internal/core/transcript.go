package core

import (
	"slices"
	"strings"
)

// Spend from Claude Code transcripts, pure (ADR 0005). The shell hands
// FoldTranscript the complete lines appended to one transcript since its
// cursor; it returns the cursor to keep and one Turn row per turn and model
// that Spent in those lines. Rows with the same Key are parts of one turn
// read in different passes and merge on read (Turn.Merge).

// Waste signal thresholds.
const (
	BloatAt       = 150000 // context tokens above which a call is bloated
	BigResult     = 40000  // tool result bytes (~10k tokens) that count as oversized
	charsPerToken = 4
)

// Turn is a row of the Profile index.
type Turn struct {
	ID       string  `json:"id"` // <session>:<n>, n counting turns from 1
	Session  string  `json:"session"`
	Parent   string  `json:"parent,omitempty"` // a subagent's main session
	Corps    string  `json:"corps"`
	Division string  `json:"division,omitempty"`
	Cwd      string  `json:"cwd,omitempty"`
	Branch   string  `json:"branch,omitempty"`
	Model    string  `json:"model"`
	Origin   string  `json:"origin"`
	Prompt   string  `json:"prompt,omitempty"`
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
	Calls    int     `json:"calls"`
	Tools    int     `json:"tools,omitempty"`
	Tokens
	USD      float64 `json:"usd"`
	Unpriced bool    `json:"unpriced,omitempty"`
	CtxMax   int64   `json:"ctx_max"`
	// Waste signals, in tokens: context above BloatAt summed over calls; cache
	// rewritten after its TTL expired; oversized tool results; re-reads of a
	// file or pane already read this session.
	Bloat   int64    `json:"bloat,omitempty"`
	Miss    int64    `json:"miss,omitempty"`
	Big     int64    `json:"big,omitempty"`
	Reread  int64    `json:"reread,omitempty"`
	BigTool string   `json:"big_tool,omitempty"` // the largest oversized result's tool
	Rereads []string `json:"rereads,omitempty"`  // what was read again
	// What the turn shipped: "commit <sha>" from git commit's output, "pr <n>"
	// from gh pr create's; Delivered units are credited by them.
	Ships []string `json:"ships,omitempty"`
}

// Key identifies the turn and model a row belongs to.
func (t Turn) Key() string { return t.ID + "|" + t.Model }

// Merge adds a later part of the same turn.
func (t *Turn) Merge(o Turn) {
	if t.Prompt == "" {
		t.Prompt = o.Prompt
	}
	if o.Start > 0 && (t.Start == 0 || o.Start < t.Start) {
		t.Start = o.Start
	}
	t.End = max(t.End, o.End)
	t.Calls += o.Calls
	t.Tools += o.Tools
	t.Tokens.Add(o.Tokens)
	t.USD += o.USD
	t.Unpriced = t.Unpriced || o.Unpriced
	t.CtxMax = max(t.CtxMax, o.CtxMax)
	t.Bloat += o.Bloat
	t.Miss += o.Miss
	if o.Big > 0 && (t.BigTool == "" || o.Big > t.Big) {
		t.BigTool = o.BigTool
	}
	t.Big += o.Big
	t.Reread += o.Reread
	for _, r := range o.Rereads {
		if len(t.Rereads) < 20 && !slices.Contains(t.Rereads, r) {
			t.Rereads = append(t.Rereads, r)
		}
	}
	for _, x := range o.Ships {
		if !slices.Contains(t.Ships, x) {
			t.Ships = append(t.Ships, x)
		}
	}
}

// Cursor is what the Profile remembers of one transcript between passes.
type Cursor struct {
	Offset  int64  `json:"offset"`
	Size    int64  `json:"size"`
	MTime   int64  `json:"mtime"`
	Session string `json:"session,omitempty"`
	Parent  string `json:"parent,omitempty"`
	Seq     int    `json:"seq,omitempty"`
	Origin  string `json:"origin,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
	// The last API call: its message id and usage (a message's usage repeats
	// on each of its content blocks), when its request started, and its TTL.
	Msg      string  `json:"msg,omitempty"`
	MsgUse   Tokens  `json:"msg_use"`
	MsgModel string  `json:"msg_model,omitempty"`
	ReqAt    float64 `json:"req_at,omitempty"`
	LastUser float64 `json:"last_user,omitempty"`
	TTL1h    bool    `json:"ttl_1h,omitempty"`
	// Tool calls awaiting their result, and how often each file or pane was read.
	Pending map[string]string `json:"pending,omitempty"`
	Reads   map[string]int    `json:"reads,omitempty"`
}

// Origins: what a turn's first prompt starts with -> Turn origin.
var originTags = []struct{ prefix, origin string }{
	{"[berthier-loop]", "loop"},
	{"[berthier ask]", "ask"},
	{"[berthier notify]", "notify"},
	{"[board]", "board"},
	{"[berthier brief]", "brief"},
	{"[berthier continue]", "continue"},
	{"[berthier resume]", "resume"},
	{"[berthier fresh]", "fresh"},
	{TagShutdown, "shutdown"},
	{TagWake, "wake"},
	{TagQuota, "quota"},
	{"<task-notification>", "task"},
}

// Tag marks a prompt Berthier sends with what sent it, so the Profile can
// tell the turn's origin; a prompt already tagged is left as it is.
func Tag(tag, text string) string {
	if strings.HasPrefix(strings.TrimSpace(text), tag) {
		return text
	}
	return tag + " " + text
}

// OriginOf is the Turn origin of a prompt: a Berthier tag, else the user.
func OriginOf(prompt string) string {
	p := strings.TrimSpace(prompt)
	for _, t := range originTags {
		if strings.HasPrefix(p, t.prefix) {
			return t.origin
		}
	}
	return "user"
}

// TranscriptFile names what a transcript file is: a main session, or a
// subagent of Parent.
type TranscriptFile struct {
	Session, Parent string
}
