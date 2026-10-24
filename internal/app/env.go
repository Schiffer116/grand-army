// Package app is Berthier's I/O shell: configuration from the environment,
// Herdr, the files of state/, and the commands. It decides nothing itself;
// internal/core does (ADR 0001, 0004).
package app

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"berthier/internal/core"
	"berthier/internal/herdr"
)

// Env is the paths and tunables every command shares.
type Env struct {
	Root, Home                                   string
	State, Log, Inbox, Reports, Demos, Divisions string
	Ledger, Usage, Budgets, BoardsFile           string
	Thaws, Briefs, NotesRead, NotesSet           string
	NotesStore                                   string
	NotesMax                                     int
	CacheWarm, FrozenMin, FreshEst, NotesLead    int64
	SessionWait                                  time.Duration
	SkipliBin, SkipliEnv                         string
	BoardPoll, BoardFailAfter                    int64
	H                                            herdr.Client
	HerdrSession                                 string
	Interval                                     string // raw, as the loop prints it
	IntervalSecs                                 float64
	BlockedAfter, LongAfter, DivisionSettle      int64
	ReviveMax                                    int
	ReviveBackoff, ReviveReset, EscalationTTL    int64
	SpeakAfter, AskStuck                         int64
	PingpongMax                                  int
	LimitGrace, LimitRetry                       int64
	DraftQuiet                                   int64
	LimitPrompt                                  string
	RestartGap, RestartWait, RestartSettle       int64
	RestartPrompt                                string
	LedgerMax                                    int64
	ProfileWeeks                                 int
	ProfileSince                                 float64
	ProfileProjects                              string
	BudgetWarn, QuotaGuardPct                    float64
	Speak                                        string
	DivisionModel                                string
	CorpsModel                                   string
	StartTimeoutMS                               int64
	PromptPoll                                   time.Duration
	ClaudeDir                                    string
	Systemctl, ConsoleURL                        string
	LandWait                                     time.Duration
	Now                                          func() time.Time
	getenv                                       func(string) string
}

// LoadEnv reads the BERTHIER_* variables (see CONTRIBUTING.md) from getenv.
// A malformed number falls back to its default. It creates state/ and its
// inbox and reports directories.
func LoadEnv(getenv func(string) string) Env {
	str := func(k, d string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return d
	}
	num := func(k string, d float64) float64 {
		if f, err := strconv.ParseFloat(getenv(k), 64); err == nil {
			return f
		}
		return d
	}
	i64 := func(k string, d int64) int64 { return int64(num(k, float64(d))) }
	home, _ := os.UserHomeDir()
	e := Env{getenv: getenv, Now: time.Now}
	e.Root = getenv("BERTHIER_ROOT")
	if e.Root == "" {
		e.Root = guessRoot()
	}
	// Runtime data lives in the repo (gitignored), next to the Berthier session.
	e.Home = str("BERTHIER_HOME", filepath.Join(e.Root, "state"))
	e.State = filepath.Join(e.Home, "state.json")
	e.Log = filepath.Join(e.Home, "loop.log")
	e.Inbox = filepath.Join(e.Home, "inbox")
	e.Reports = filepath.Join(e.Home, "reports")
	e.Demos = filepath.Join(e.Home, "demos")
	e.Divisions = filepath.Join(e.Home, "divisions.json")
	e.Ledger = filepath.Join(e.Home, "ledger.jsonl")
	e.Usage = filepath.Join(e.Home, "usage.json")
	e.Budgets = filepath.Join(e.Home, "budgets.json")
	e.BoardsFile = filepath.Join(e.Home, "boards.json")
	e.Thaws = filepath.Join(e.Home, "thaw")
	e.Briefs = filepath.Join(e.Home, "briefs")
	e.NotesRead = filepath.Join(e.Home, "notes-read.json")
	e.NotesSet = filepath.Join(e.Home, "notes-set.json")
	// Empty means the user's default Herdr session; tests point this at an
	// isolated named session so they never touch live workspaces.
	e.HerdrSession = getenv("BERTHIER_HERDR_SESSION")
	e.Interval = str("BERTHIER_INTERVAL", "10")
	e.IntervalSecs = num("BERTHIER_INTERVAL", 10)
	e.BlockedAfter = i64("BERTHIER_BLOCKED_AFTER", 120)
	e.LongAfter = i64("BERTHIER_LONG_AFTER", 3600)
	e.DivisionSettle = i64("BERTHIER_DIVISION_SETTLE", 30)
	e.ReviveMax = int(i64("BERTHIER_REVIVE_MAX", 3))
	e.ReviveBackoff = i64("BERTHIER_REVIVE_BACKOFF", 60)
	e.ReviveReset = i64("BERTHIER_REVIVE_RESET", 300)
	e.EscalationTTL = i64("BERTHIER_ESCALATION_TTL", 604800)
	e.SpeakAfter = i64("BERTHIER_SPEAK_AFTER", 30)
	// Desktop notification + chime + TTS, from the dotfiles' notify-user skill.
	e.Speak = str("BERTHIER_SPEAK", filepath.Join(home, ".claude/skills/notify-user/speak.sh"))
	e.AskStuck = i64("BERTHIER_ASK_STUCK", 1800)
	e.PingpongMax = int(i64("BERTHIER_PINGPONG_MAX", 10))
	// Usage limits: nudge a stopped Agent this long after its limit resets,
	// and never twice within RETRY (a nudge that hits the limit again starts over).
	e.LimitGrace = i64("BERTHIER_LIMIT_GRACE", 60)
	e.LimitRetry = i64("BERTHIER_LIMIT_RETRY", 1800)
	e.LimitPrompt = str("BERTHIER_LIMIT_PROMPT", "The usage limit has reset. Continue exactly where you left off.")
	// Prompts to a pane wait this long after its input box last held the
	// user's draft: a pause in dictation is not the end of it.
	e.DraftQuiet = i64("BERTHIER_DRAFT_QUIET", 20)
	// Restart: a gap this long since the last state write (loop down, Herdr
	// unreachable) may have cut Agents off mid-turn; wait this long for each to
	// reappear, then prompt it once.
	e.RestartGap = i64("BERTHIER_RESTART_GAP", 45)
	e.RestartWait = i64("BERTHIER_RESTART_WAIT", 3600)
	e.RestartSettle = i64("BERTHIER_RESTART_SETTLE", 20)
	e.RestartPrompt = str("BERTHIER_RESTART_PROMPT", "The machine restarted and interrupted your turn; continue where you left off.")
	e.LedgerMax = i64("BERTHIER_LEDGER_MAX", 4194304)
	// The Profile keeps this many quota weeks of turns (the current one
	// included), and never reads transcripts from before PROFILE_SINCE (epoch
	// seconds; tests set it to their start).
	e.ProfileWeeks = int(max(i64("BERTHIER_PROFILE_WEEKS", 5), 1))
	e.ProfileSince = num("BERTHIER_PROFILE_SINCE", 0)
	// Which of Claude's project directories (a glob) the Profile reads.
	e.ProfileProjects = str("BERTHIER_PROFILE_PROJECTS", "*")
	// Share of its budget at which a Corps's Main Agent is told and the user hears (percent).
	e.BudgetWarn = num("BERTHIER_BUDGET_WARN", 80)
	e.QuotaGuardPct = num("BERTHIER_QUOTA_GUARD_PCT", 90)
	// Boards: skipli-cli, on one Skipli Environment (dev for now), polled this often (seconds).
	e.SkipliBin = str("BERTHIER_SKIPLI_BIN", "skipli-cli")
	e.SkipliEnv = str("BERTHIER_SKIPLI_ENV", "dev")
	e.BoardPoll = i64("BERTHIER_BOARD_POLL", 60)
	// A Board that cannot be read is escalated once it has failed this long (seconds); a missing Session at once.
	e.BoardFailAfter = i64("BERTHIER_BOARD_FAIL_AFTER", 300)
	// Where every Corps keeps its Notes (Army-wide; only skipli for now: on its Board), and their cap
	// in bytes (Skipli takes a change of up to 128KiB; Notes are read by every new session).
	e.NotesStore = str("BERTHIER_NOTES_STORE", "skipli")
	e.NotesMax = int(i64("BERTHIER_NOTES_MAX", 32768))
	// How long session-start may take to read the Notes before it prints without them (seconds).
	e.SessionWait = time.Duration(num("BERTHIER_SESSION_START_WAIT", 5) * float64(time.Second))
	// A Revive or usage-limit resume goes ahead by itself only if the session's last API call was
	// under CACHE_WARM seconds ago (Claude Code's cache TTL is an hour) or its context is under
	// FROZEN_MIN tokens; otherwise the Agent is Frozen until the user chooses.
	e.CacheWarm = i64("BERTHIER_CACHE_WARM", 3300)
	e.FrozenMin = i64("BERTHIER_FROZEN_MIN", 50000)
	e.FreshEst = i64("BERTHIER_FRESH_EST_TOKENS", 60000)
	// An idle Agent whose last API call is CACHE_WARM-NOTES_LEAD seconds old (cache nearly cold) is
	// asked once per idle episode to update its Corps's stale Notes; 0 turns it off.
	e.NotesLead = i64("BERTHIER_NOTES_LEAD", 300)
	// Claude model of spawned Divisions (division-new --model overrides); Main
	// Agents and Berthier use claude's own default.
	e.DivisionModel = str("BERTHIER_DIVISION_MODEL", "haiku")
	e.CorpsModel = str("BERTHIER_CORPS_MODEL", "opus")
	e.StartTimeoutMS = i64("BERTHIER_START_TIMEOUT_MS", 45000)
	// How often prompt_agent checks that Claude took a prompt (seconds).
	e.PromptPoll = time.Duration(num("BERTHIER_PROMPT_POLL", 0.5) * float64(time.Second))
	// Claude Code's own directory: live sessions (sessions/<pid>.json) and transcripts.
	e.H = herdr.FromEnv(getenv, e.PromptPoll)
	e.ClaudeDir = str("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	// `land` restarts services with this and waits up to LandWait for them to work.
	e.Systemctl = str("BERTHIER_SYSTEMCTL", "systemctl")
	e.ConsoleURL = str("BERTHIER_CONSOLE_URL", "http://127.0.0.1:8430/")
	e.LandWait = time.Duration(num("BERTHIER_LAND_WAIT", 120) * float64(time.Second))
	for _, d := range []string{e.Home, e.Inbox, e.Reports, e.Demos, e.Thaws} {
		os.MkdirAll(d, 0o755)
	}
	return e
}

// guessRoot is the repository the running binary lives in (<root>/bin/berthier).
func guessRoot() string {
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			return filepath.Dir(filepath.Dir(exe))
		}
	}
	wd, _ := os.Getwd()
	return wd
}

func (e *Env) now() int64 { return e.Now().Unix() }

// Config is the Main Loop's tunables for core.Tick.
func (e *Env) CoreConfig() (c core.Config) {
	return core.Config{BerthierRoot: e.Root, BlockedAfter: e.BlockedAfter, LongAfter: e.LongAfter, DivisionSettle: e.DivisionSettle,
		ReviveMax: e.ReviveMax, ReviveBackoff: e.ReviveBackoff, ReviveReset: e.ReviveReset, EscalationTTL: e.EscalationTTL,
		SpeakAfter: e.SpeakAfter, AskStuck: e.AskStuck, PingpongMax: e.PingpongMax, LimitGrace: e.LimitGrace,
		LimitRetry: e.LimitRetry, BudgetWarn: e.BudgetWarn, QuotaGuardPct: e.QuotaGuardPct, DraftQuiet: e.DraftQuiet,
		RestartGap: e.RestartGap, RestartWait: e.RestartWait, RestartSettle: e.RestartSettle,
		CacheWarm: e.CacheWarm, FrozenMin: e.FrozenMin, NotesLead: e.NotesLead}
}
