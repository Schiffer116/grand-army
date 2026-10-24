// Package claude is the Harness adapter for Claude Code (ADR 0006): its launch
// flags, keys, screens, hooks and transcripts. Everything that knows how Claude
// Code looks or behaves lives here.
package claude

import (
	"path/filepath"
	"sync"
	"time"

	"berthier/internal/core"
	"berthier/internal/harness"
	"berthier/internal/host"
)

// Claude is Claude Code, configured in ConfigDir (CLAUDE_CONFIG_DIR, ~/.claude).
type Claude struct {
	ConfigDir string
}

var _ harness.Harness = Claude{}

func (Claude) Launch(o harness.LaunchOpts) []string {
	argv := []string{"claude"}
	if o.Resume != "" {
		argv = append(argv, "--resume", o.Resume)
	}
	if o.Model != "" {
		argv = append(argv, "--model", o.Model)
	}
	for _, t := range o.Disallow {
		argv = append(argv, "--disallowedTools", t)
	}
	return argv
}

func (Claude) ClearCommand() string { return "/clear" }

// Keys: Esc interrupts a turn (Ctrl-C twice exits Claude), Ctrl-U clears the
// input box, and two Ctrl-C quit.
func (Claude) Keys(i harness.Intent) []host.Key {
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

func (Claude) Draft(screen string) bool            { return InputDraft(screen) }
func (Claude) PromptHeld(screen, text string) bool { return InputHolds(screen, text) }
func (Claude) UsageLimit(screen string, since int64, local *time.Location) (int64, bool) {
	return UsageLimitReset(screen, since, local)
}
func (Claude) ExitedDeliberately(tail, session string) bool { return ExitedDeliberately(tail, session) }

func (c Claude) Transcripts(session string) []string {
	m, _ := filepath.Glob(filepath.Join(c.ConfigDir, "projects", "*", session+".jsonl"))
	return m
}

func (c Claude) AllTranscripts(projects string) []string {
	root := filepath.Join(c.ConfigDir, "projects")
	a, _ := filepath.Glob(filepath.Join(root, projects, "*.jsonl"))
	b, _ := filepath.Glob(filepath.Join(root, projects, "*", "subagents", "*.jsonl"))
	return append(a, b...)
}

func (Claude) TranscriptFile(path string) core.TranscriptFile {
	id := trimExt(filepath.Base(path))
	if filepath.Base(filepath.Dir(path)) == "subagents" {
		parent := filepath.Base(filepath.Dir(filepath.Dir(path)))
		return core.TranscriptFile{Session: parent + "/" + id, Parent: parent}
	}
	return core.TranscriptFile{Session: id}
}

func (Claude) Spend(c core.Cursor, f core.TranscriptFile, lines [][]byte, place harness.Placer) (core.Cursor, []core.Turn) {
	return FoldTranscript(c, f, lines, place)
}

func (Claude) Interrupted(lines []string) bool           { return TranscriptInterrupted(lines) }
func (Claude) CacheOf(lines []string) (core.Cache, bool) { return CacheOf(lines) }

func trimExt(name string) string { return name[:len(name)-len(filepath.Ext(name))] }

// chats keeps one incremental chat reader per Claude directory, so a poll reads
// only the lines appended since the last.
var chats sync.Map

func (c Claude) transcripts() *Transcripts {
	v, _ := chats.LoadOrStore(c.ConfigDir, NewTranscripts(c.ConfigDir))
	return v.(*Transcripts)
}

func (c Claude) Chat(session string) (harness.Chat, bool) {
	t := c.transcripts()
	path := t.Path(session)
	if path == "" {
		return harness.Chat{}, false
	}
	items, rev, err := t.Read(path)
	if err != nil {
		return harness.Chat{}, false
	}
	return harness.Chat{Session: trimExt(filepath.Base(path)), Rev: rev, Items: items, Info: t.Info(path)}, true
}

func (c Claude) LastMessage(session string) (harness.ChatItem, bool) {
	t := c.transcripts()
	path := t.Path(session)
	if path == "" {
		return harness.ChatItem{}, false
	}
	return t.Last(path)
}

func (c Claude) Skills(cwd string) []harness.Skill { return Skills(c.ConfigDir, cwd) }
