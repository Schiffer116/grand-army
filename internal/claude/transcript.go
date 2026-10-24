package claude

import (
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"berthier/internal/core"
)

// Claude Code's transcripts: Spend (ADR 0005), the end of a session, its
// prompt cache.

const charsPerToken = 4

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

// FoldTranscript folds a transcript's new complete lines into turn rows,
// placing each turn by its cwd. A file's first pass starts from a zero Cursor.
func FoldTranscript(c core.Cursor, f core.TranscriptFile, lines [][]byte, place func(cwd string) (string, string, bool)) (core.Cursor, []core.Turn) {
	if c.Session == "" {
		c.Session, c.Parent = f.Session, f.Parent
		if c.Parent != "" {
			c.Origin = "subagent"
		}
	}
	// Collect, not Clone: the maps are written to and Clone keeps nil
	c.Pending, c.Reads = maps.Collect(maps.All(c.Pending)), maps.Collect(maps.All(c.Reads))
	rows := map[string]*core.Turn{}
	var order []string
	row := func(l *tLine, model string, ts float64) *core.Turn {
		if c.Seq == 0 { // calls before any prompt (a resumed session): turn 1
			c.Seq = 1
			if c.Origin == "" {
				c.Origin = "user"
			}
		}
		t := core.Turn{ID: c.Session + ":" + itoa(c.Seq), Model: model}
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
	var last *core.Turn // the row of the call the next tool results belong to
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
							if !slices.Contains(to.Ships, x) {
								to.Ships = append(to.Ships, x)
							}
						}
					}
				}
				if n > core.BigResult && last != nil {
					last.Big += n / charsPerToken
					if last.BigTool == "" {
						last.BigTool = c.Pending[b.ToolUseID+"#tool"]
					}
				}
				if key := c.Pending[b.ToolUseID]; key != "" {
					if c.Reads[key] > 0 && last != nil {
						last.Reread += n / charsPerToken
						if len(last.Rereads) < 20 && !slices.Contains(last.Rereads, key) {
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
				c.Origin, c.Prompt = core.OriginOf(text), clip(oneLine(text), 100)
				last = nil
			} else if isText && c.Parent != "" && c.Seq == 0 {
				c.Seq, c.Origin, c.Prompt = 1, "subagent", clip(oneLine(text), 100)
			}
		case "assistant":
			u := l.Message.Usage
			if u == nil || l.Message.Model == "" || l.Message.Model == "<synthetic>" {
				continue
			}
			tok := core.Tokens{In: u.In, CR: u.CacheRead, Out: u.Out}
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
			if ctx > core.BloatAt {
				t.Bloat += ctx - core.BloatAt
			}
		}
	}
	out := make([]core.Turn, 0, len(order))
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

func callUSD(model string, t core.Tokens, speed string) (float64, bool) {
	p, ok := core.PriceOf(model)
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

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func itoa(n int) string { return core.Num(float64(n)) }

// CacheOf reads a transcript's last lines for its last real API call
// (Claude's own synthetic messages, such as a usage-limit stop, call nothing).
func CacheOf(lines []string) (core.Cache, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		if !strings.Contains(lines[i], `"usage"`) {
			continue
		}
		var l tLine
		if json.Unmarshal([]byte(lines[i]), &l) != nil || l.Type != "assistant" || l.Message.Usage == nil ||
			l.Message.Model == "" || strings.HasPrefix(l.Message.Model, "<") {
			continue
		}
		u := l.Message.Usage
		if u.In+u.CacheRead+u.CacheCreation == 0 {
			continue
		}
		c := core.Cache{LastAt: int64(parseTS(l.Timestamp)), Context: u.In + u.CacheRead + u.CacheCreation + u.Out, Model: l.Message.Model}
		// Claude Code writes with a 1-hour TTL unless the split says otherwise.
		c.TTL1h = u.Split == nil || u.Split.W1h > 0 || u.Split.W5m == 0
		return c, true
	}
	return core.Cache{}, false
}

// TranscriptInterrupted: did a session's transcript end mid-turn (a tool
// result, a task notification or a tool call nobody answered) rather than on
// a finished assistant turn or the user's own interrupt? lines are the end of
// the transcript (JSON Lines). No message at all counts as finished.
func TranscriptInterrupted(lines []string) bool {
	for i := len(lines) - 1; i >= 0; i-- {
		var m struct {
			Type    string `json:"type"`
			Message struct {
				StopReason string          `json:"stop_reason"`
				Content    json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(lines[i]), &m) != nil || (m.Type != "user" && m.Type != "assistant") {
			continue
		}
		if m.Type == "assistant" {
			return m.Message.StopReason != "end_turn"
		}
		text := string(m.Message.Content)
		var s string
		var parts []struct{ Text string }
		if json.Unmarshal(m.Message.Content, &s) == nil {
			text = s
		} else if json.Unmarshal(m.Message.Content, &parts) == nil {
			var t []string
			for _, p := range parts {
				t = append(t, p.Text)
			}
			text = strings.Join(t, " ")
		}
		return !strings.Contains(text, "[Request interrupted by user")
	}
	return false
}
