package core

import (
	"fmt"
	"strings"
)

// Frozen Agents: a Revive (or a usage-limit resume) resumes the session, and
// with its prompt cache gone cold that rewrites its whole context. The Main
// Loop does it by itself only while the cache is warm or the context small;
// otherwise the Agent is Frozen until the user chooses Revive or Fresh Start.

// Cache is what a session's transcript says about its prompt cache: its last
// API call, the context the next call sends (Context tokens), and the model.
type Cache struct {
	LastAt  int64  `json:"last_at"`
	Context int64  `json:"context"`
	Model   string `json:"model,omitempty"`
	TTL1h   bool   `json:"ttl_1h,omitempty"`
}

// Frozen is an Agent due a Revive (Why "crash") or a usage-limit resume
// ("limit") whose cache went cold: nothing is resumed or delivered to it until
// the user chooses.
type Frozen struct {
	Since int64  `json:"since"`
	Why   string `json:"why"`
	Auto  string `json:"auto,omitempty"` // "refused:<why>": no automatic Fresh Start for this episode
	Cache
}

// Warm: resuming now reads the cache, or the context is too small to matter.
func (c Cache) Warm(now int64, cfg Config) bool {
	return now-c.LastAt < cfg.CacheWarm || c.Context < cfg.FrozenMin
}

// ReviveUSD estimates what resuming a cold session costs: its whole context
// written to the cache again (at the TTL Claude Code used for it).
func (c Cache) ReviveUSD() (float64, bool) { return c.WriteUSD(c.Context) }

// WriteUSD prices n tokens written to the cache, as ReviveUSD does the context.
func (c Cache) WriteUSD(n int64) (float64, bool) {
	p, ok := PriceOf(c.Model)
	if !ok {
		return 0, false
	}
	t := Tokens{W5m: n}
	if c.TTL1h {
		t = Tokens{W1h: n}
	}
	return p.USD(t, false), true
}

// FreshPrompt is the first prompt of a Fresh Start: rebuild from what is
// written down. A Division also gets its Brief again.
func FreshPrompt(report, brief string) string {
	return "[berthier fresh] You were restarted fresh: run berthier notes, read your Board, your last report and git status, then continue." + Rebuild(report, brief)
}

// Rebuild is what an Agent with no context left needs besides its Notes, Board
// and git: its last report and, for a Division, its Brief again.
func Rebuild(report, brief string) string {
	p := ""
	if report != "" {
		p += fmt.Sprintf("\nYour last report: %s", report)
	}
	if brief != "" {
		p += "\nYour branch holds your progress so far. Your original Brief:\n\n" + brief
	}
	return p
}

// Thaw is the user's choice for a Frozen Agent (`berthier revive|fresh`, the
// Console): How is "revive" or "fresh".
type Thaw struct {
	ID   string `json:"id"`
	Pane string `json:"pane"`
	How  string `json:"how"`
	At   int64  `json:"at"`
}

// FreshStart replaces a Frozen Agent's session with a new one in its pane.
// Live: its Claude still runs (stopped on its usage limit) and must exit first.
type FreshStart struct {
	PaneID   string `json:"pane_id"`
	Name     string `json:"name"`
	Session  string `json:"session"` // the frozen session, whose last report the prompt carries
	Model    string `json:"model,omitempty"`
	Division string `json:"division,omitempty"` // its Division, whose Brief it gets again
	Label    string `json:"label,omitempty"`    // its workspace label: decides its launch flags
	Live     bool   `json:"live,omitempty"`
	Ev       Fields `json:"ev"`
}

// tokensK is a token count for people: 183k.
func tokensK(n int64) string { return fmt.Sprintf("%dk", (n+500)/1000) }

// AutoCheck is what the shell read for an automatic Fresh Start of a Frozen
// Division: whether its Brief is kept, and git's verdict on its worktree.
type AutoCheck struct {
	Brief bool   `json:"brief"`
	Dirty string `json:"dirty,omitempty"` // `git status --porcelain` output
	Err   string `json:"err,omitempty"`   // git could not tell
}

// Verdict: a Fresh Start by itself only when it can lose nothing. Committed
// WIP counts as clean (the branch keeps it); uncommitted changes do not.
func (c AutoCheck) Verdict() (bool, string) {
	switch {
	case !c.Brief:
		return false, "no Brief kept for it"
	case c.Err != "":
		return false, "git status failed: " + c.Err
	case strings.TrimSpace(c.Dirty) != "":
		return false, "its worktree has uncommitted changes"
	}
	return true, ""
}
