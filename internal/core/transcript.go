package core

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
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
		if len(t.Rereads) < 20 && !inStrs(t.Rereads, r) {
			t.Rereads = append(t.Rereads, r)
		}
	}
	for _, x := range o.Ships {
		if !inStrs(t.Ships, x) {
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

type tLine struct {
	Type             string `json:"type"`
	IsMeta           bool   `json:"isMeta"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	SessionID        string `json:"sessionId"`
	AgentID          string `json:"agentId"`
	Cwd              string `json:"cwd"`
	GitBranch        string `json:"gitBranch"`
	Timestamp        string `json:"timestamp"`
	Message          struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			In            int64 `json:"input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			Out           int64 `json:"output_tokens"`
			Split         *struct {
				W5m int64 `json:"ephemeral_5m_input_tokens"`
				W1h int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
			Speed string `json:"speed"`
		} `json:"usage"`
	} `json:"message"`
}

type tBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

// TranscriptFile names what a transcript file is: a main session, or a
// subagent of Parent.
type TranscriptFile struct {
	Session, Parent string
}

// FoldTranscript folds a transcript's new complete lines into turn rows,
// placing each turn by its cwd. A file's first pass starts from a zero Cursor.
func FoldTranscript(c Cursor, f TranscriptFile, lines [][]byte, place func(cwd string) (string, string, bool)) (Cursor, []Turn) {
	if c.Session == "" {
		c.Session, c.Parent = f.Session, f.Parent
		if c.Parent != "" {
			c.Origin = "subagent"
		}
	}
	c.Pending, c.Reads = copyStrMap(c.Pending), copyIntMap(c.Reads)
	rows := map[string]*Turn{}
	var order []string
	row := func(l *tLine, model string, ts float64) *Turn {
		if c.Seq == 0 { // calls before any prompt (a resumed session): turn 1
			c.Seq = 1
			if c.Origin == "" {
				c.Origin = "user"
			}
		}
		t := Turn{ID: c.Session + ":" + itoa(c.Seq), Model: model}
		if r, ok := rows[t.Key()]; ok {
			return r
		}
		t.Session, t.Parent, t.Origin, t.Prompt, t.Cwd, t.Branch, t.Start = c.Session, c.Parent, c.Origin, c.Prompt, l.Cwd, l.GitBranch, ts
		corps, div, ok := place(l.Cwd)
		if !ok {
			corps = "other"
		}
		t.Corps, t.Division = corps, div
		rows[t.Key()] = &t
		order = append(order, t.Key())
		return &t
	}
	var last *Turn // the row of the call the next tool results belong to
	for _, raw := range lines {
		var l tLine
		if json.Unmarshal(raw, &l) != nil {
			continue
		}
		ts := parseTS(l.Timestamp)
		switch l.Type {
		case "user":
			c.LastUser = max(c.LastUser, ts)
			var blocks []tBlock
			text, isText := "", false
			if len(l.Message.Content) > 0 && l.Message.Content[0] == '"' {
				json.Unmarshal(l.Message.Content, &text)
				isText = true
			} else {
				json.Unmarshal(l.Message.Content, &blocks)
				for _, b := range blocks {
					if b.Type == "text" && !isText {
						text, isText = b.Text, true
					}
				}
			}
			for _, b := range blocks {
				if b.Type != "tool_result" {
					continue
				}
				text := resultText(b.Content)
				n := int64(len(text))
				if ship := c.Pending[b.ToolUseID+"#ship"]; ship != "" {
					to := last
					if to == nil && c.MsgModel != "" {
						to = row(&l, c.MsgModel, ts)
					}
					if to != nil {
						for _, x := range shipped(ship, text) {
							if !inStrs(to.Ships, x) {
								to.Ships = append(to.Ships, x)
							}
						}
					}
				}
				if n > BigResult && last != nil {
					last.Big += n / charsPerToken
					if last.BigTool == "" {
						last.BigTool = c.Pending[b.ToolUseID+"#tool"]
					}
				}
				if key := c.Pending[b.ToolUseID]; key != "" {
					if c.Reads[key] > 0 && last != nil {
						last.Reread += n / charsPerToken
						if len(last.Rereads) < 20 && !inStrs(last.Rereads, key) {
							last.Rereads = append(last.Rereads, key)
						}
					}
					if len(c.Reads) < 2000 {
						c.Reads[key]++
					}
				}
				delete(c.Pending, b.ToolUseID)
				delete(c.Pending, b.ToolUseID+"#tool")
				delete(c.Pending, b.ToolUseID+"#ship")
			}
			if isText && !l.IsMeta && !l.IsCompactSummary && c.Parent == "" && !strings.HasPrefix(strings.TrimSpace(text), "<local-command") && !strings.HasPrefix(strings.TrimSpace(text), "<command-") {
				c.Seq++
				c.Origin, c.Prompt = OriginOf(text), clip(oneLineStr(text), 100)
				last = nil
			} else if isText && c.Parent != "" && c.Seq == 0 {
				c.Seq, c.Origin, c.Prompt = 1, "subagent", clip(oneLineStr(text), 100)
			}
		case "assistant":
			u := l.Message.Usage
			if u == nil || l.Message.Model == "" || l.Message.Model == "<synthetic>" {
				continue
			}
			tok := Tokens{In: u.In, CR: u.CacheRead, Out: u.Out}
			if u.Split != nil {
				tok.W5m, tok.W1h = u.Split.W5m, u.Split.W1h
				tok.Unknown = max(u.CacheCreation-tok.W5m-tok.W1h, 0)
			} else {
				tok.Unknown = u.CacheCreation
			}
			t := row(&l, l.Message.Model, ts)
			last = t
			t.End = max(t.End, ts)
			// Tool calls of this content block.
			var blocks []tBlock
			json.Unmarshal(l.Message.Content, &blocks)
			for _, b := range blocks {
				if b.Type != "tool_use" {
					continue
				}
				t.Tools++
				c.Pending[b.ID+"#tool"] = b.Name
				if key := readKey(b.Name, b.Input); key != "" {
					c.Pending[b.ID] = key
				}
				if ship := shipKind(b.Name, b.Input); ship != "" {
					c.Pending[b.ID+"#ship"] = ship
				}
			}
			if l.Message.ID != "" && l.Message.ID == c.Msg {
				// Another block of the same message: count only what grew.
				d := tok.Sub(c.MsgUse)
				if d.In < 0 || d.CR < 0 || d.W5m < 0 || d.W1h < 0 || d.Out < 0 || d.Unknown < 0 {
					continue
				}
				c.MsgUse = tok
				t.Tokens.Add(d)
				usd, priced := callUSD(l.Message.Model, tok, u.Speed)
				prev, _ := callUSD(l.Message.Model, tok.Sub(d), u.Speed)
				t.USD += usd - prev
				t.Unpriced = t.Unpriced || !priced
				continue
			}
			// A new API call.
			req := c.LastUser
			if req == 0 || req > ts {
				req = ts
			}
			ttl := 300.0
			if c.TTL1h {
				ttl = 3600
			}
			if c.ReqAt > 0 && req-c.ReqAt > ttl && tok.Writes() > 0 {
				t.Miss += tok.Writes()
			}
			c.ReqAt, c.Msg, c.MsgUse, c.MsgModel = req, l.Message.ID, tok, l.Message.Model
			if tok.W1h > 0 {
				c.TTL1h = true
			} else if tok.W5m > 0 {
				c.TTL1h = false
			}
			t.Calls++
			t.Tokens.Add(tok)
			usd, priced := callUSD(l.Message.Model, tok, u.Speed)
			t.USD += usd
			t.Unpriced = t.Unpriced || !priced
			ctx := tok.Prompt()
			t.CtxMax = max(t.CtxMax, ctx)
			if ctx > BloatAt {
				t.Bloat += ctx - BloatAt
			}
		}
	}
	out := make([]Turn, 0, len(order))
	for _, k := range order {
		out = append(out, *rows[k])
	}
	return c, out
}

// resultText is the text of a tool result; images are left out (they cost
// tokens by size in pixels, not bytes).
func resultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		json.Unmarshal(raw, &s)
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

var (
	shipCommit  = regexp.MustCompile(`\bgit\b[^|;&]*\bcommit\b`)
	shipPR      = regexp.MustCompile(`\bpr[ -]create\b`)
	commitLine  = regexp.MustCompile(`(?m)^\[[^\s\]]+(?: \([^)]*\))? ([0-9a-f]{7,40})\] `)
	pullRequest = regexp.MustCompile(`github\.com/[^/\s]+/[^/\s]+/pull/(\d+)`)
)

// shipKind is what a tool call may ship: a commit, a pull request, or nothing.
func shipKind(tool string, input json.RawMessage) string {
	if tool != "Bash" {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	json.Unmarshal(input, &in)
	switch {
	case shipPR.MatchString(in.Command):
		return "pr"
	case shipCommit.MatchString(in.Command):
		return "commit"
	}
	return ""
}

// shipped reads what a commit or pull request call's output says it made.
func shipped(kind, out string) []string {
	var l []string
	if kind == "commit" || kind == "pr" {
		for _, m := range commitLine.FindAllStringSubmatch(out, -1) {
			l = append(l, "commit "+m[1][:7])
		}
	}
	if kind == "pr" {
		for _, m := range pullRequest.FindAllStringSubmatch(out, -1) {
			l = append(l, "pr "+m[1])
		}
	}
	return l
}

func callUSD(model string, t Tokens, speed string) (float64, bool) {
	p, ok := PriceOf(model)
	if !ok {
		return 0, false
	}
	return p.USD(t, speed == "fast"), true
}

var paneRead = regexp.MustCompile(`(?:agent read|pane read|berthier last)\s+(\S+)`)

// readKey names what a tool call reads, for counting re-reads: a file, or a pane.
func readKey(tool string, input json.RawMessage) string {
	var in struct {
		FilePath string `json:"file_path"`
		Command  string `json:"command"`
		Offset   any    `json:"offset"`
	}
	json.Unmarshal(input, &in)
	switch {
	case tool == "Read" && in.FilePath != "" && in.Offset == nil:
		return in.FilePath
	case tool == "Bash":
		if m := paneRead.FindStringSubmatch(in.Command); m != nil && !strings.ContainsAny(m[1], "$`") {
			return "pane " + strings.Trim(m[1], `"'`)
		}
	}
	return ""
}

func parseTS(s string) float64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return float64(t.UnixMilli()) / 1000
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func oneLineStr(s string) string { return strings.Join(strings.Fields(s), " ") }

func itoa(n int) string { return Num(float64(n)) }

func copyStrMap(m map[string]string) map[string]string {
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func copyIntMap(m map[string]int) map[string]int {
	c := make(map[string]int, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}
