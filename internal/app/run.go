package app

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// IO is a command's standard streams.
type IO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// UsageError is a bad command line: printed with the usage, exit 2.
type UsageError struct{ Msg string }

func (e UsageError) Error() string { return e.Msg }

// exitCode makes a command end with a status and no message of its own.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func die(format string, a ...any) error { return fmt.Errorf(format, a...) }

const usageText = `usage: berthier <command> [args]

corps-new <name> <dir> [brief]      create a Corps workspace with a Main Agent
adopt <workspace> <corps>           make an existing workspace a Division of a Corps
board <corps> [--adopt <board-id>]  give a Corps its Board: an existing Skipli board, else a card on the Grand Army Board;
                                    the Main Loop hands its Main Agent new tasks added there (corps-new does this itself)
division-new <name> [--model <m>] [--artifact] <brief>
                                    (Main Agent) spawn a Division: a worktree workspace with its own Agent
                                    on --model (default $BERTHIER_DIVISION_MODEL, haiku); no Artifact unless --artifact
land <division>                     (Berthier) merge a Division of Berthier into master, check, rebuild, restart
                                    the changed services and wait until they work; refuses on conflict or failure
division-done <name>                (Main Agent) remove a spawned Division once the user reviewed it
report "<line>"                     (any Agent) one-line status for the user; "needs you: ..." when they must act
demo <url> [label]                (any Agent) where this Agent's demo or preview is hosted, shown in the Console's info panel;
                                    --clear [url] drops one (or all), --list prints them
ask <corps> "<request>"             (any Agent) request work from another Corps, delivered when it is idle
notify <corps> "<text>"             (service, no pane needed) one-way message to a Corps's Main Agent, no reply expected
shutdown [text]                     (the user's, army-wide) tell every Agent with a live pane (Frozen skipped) to wrap up: park, commit+push WIP, report, update notes
wake                                (the user's, army-wide) /clear every such Agent, then tell it to rebuild and resume, after a restart
prompt <pane> <text>|-              prompt the Agent in a pane and make sure Claude took it (not a bare herdr agent prompt);
                                    - reads the text from stdin
last <pane>                         the Claude Agent's latest assistant message, from its transcript (no pane read)
revive <pane>                       (the user's choice) resume a Frozen Agent's session anyway, rewriting its cache
fresh <pane>                        (the user's choice) a Fresh Start for a Frozen Agent: a new session in its pane, same model,
                                    rebuilt from its Corps's Notes, Board, last report and git (a Division: also its Brief)
session-start                       Claude Code SessionStart hook (JSON on stdin): Berthier's repo root gets BERTHIER.md and
                                    the Berthier Notes; a Corps's Main Agent its Corps's Notes; anything else nothing; never fails
notes [<corps>] [--set -]           a Corps's Notes (default: the caller's); --set - replaces them from stdin, refused if
                                    they changed since this Agent last read them (read again and merge)
owners                              who owns what, to know whom to ask
sample                              (status line) read session JSON on stdin, append to the quota ledger; silent, never fails
budget [<corps> <pct>|off]          weekly quota used vs allowance per Corps; set or drop one Corps's budget (% of the week)
profile [--waste] [--corps <c>] [--session <id>] [--week <n>]
                                    Spend of a quota week (JSON): Corps -> Division -> session, by model, token kind,
                                    cache hit and Turn origin, Spend per Delivered unit, ranked Waste signals;
                                    --waste: only the Waste signals, all of them; --session: that session's turns
counts                              Agent count per status plus reports, as JSON for status bars
log [-n lines] [-f]                 the Main Loop's event log, readable
army                                the full Grand Army view model as JSON (Corps, Divisions, Agents, asks, log)
dashboard [server args]             serve the live Grand Army console on 127.0.0.1:8430
hook-stop                           Claude Code Stop hook: a turn that ends unreported gets a report from its final message
tick                                run one Main Loop pass
loop                                run the Main Loop forever
`

// Run runs one berthier command and returns its exit status.
func Run(args []string, io IO, getenv func(string) string) int {
	if len(args) == 0 {
		fmt.Fprint(io.Err, usageText)
		return 2
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Fprint(io.Out, usageText)
		return 0
	case "sample":
		// Runs on every status-line render: silent, fast, never fails.
		defer func() { recover() }()
		sample(getenv, io.In)
		return 0
	}
	run, ok := commands[cmd]
	if !ok {
		fmt.Fprint(io.Err, usageText)
		return 2
	}
	e := LoadEnv(getenv)
	err := run(&e, args, io)
	switch x := err.(type) {
	case nil:
		return 0
	case exitCode:
		return int(x)
	case UsageError:
		fmt.Fprintf(io.Err, "berthier: %s\n", x.Msg)
		return 1
	}
	if err == ErrHerdrUnreachable {
		return 3
	}
	fmt.Fprintf(io.Err, "berthier: %s\n", err)
	return 1
}

// Main is the binary's entry point.
func Main() {
	os.Exit(Run(os.Args[1:], IO{os.Stdin, os.Stdout, os.Stderr}, os.Getenv))
}

type command func(e *Env, args []string, io IO) error

var commands map[string]command

func init() {
	commands = map[string]command{
		"corps-new":          cmdCorpsNew,
		"adopt":              cmdAdopt,
		"board":              cmdBoard,
		"army":               cmdArmy,
		"dashboard":          cmdDashboard,
		"division-new":       cmdDivisionNew,
		"division-done":      cmdDivisionDone,
		"land":               cmdLand,
		"report":             cmdReport,
		"demo":               cmdDemo,
		"ask":                cmdAsk,
		"notify":             cmdNotify,
		"shutdown":           cmdBroadcast("shutdown"),
		"wake":               cmdBroadcast("wake"),
		"prompt":             cmdPrompt,
		"last":               cmdLast,
		"revive":             cmdThaw("revive"),
		"fresh":              cmdThaw("fresh"),
		"notes":              cmdNotes,
		"session-start":      cmdSessionStart,
		"owners":             cmdOwners,
		"counts":             cmdCounts,
		"budget":             cmdBudget,
		"profile":            cmdProfile,
		"log":                cmdLog,
		"hook-stop":          cmdHookStop,
		"tick":               func(e *Env, a []string, io IO) error { return e.Tick() },
		"loop":               func(e *Env, a []string, io IO) error { e.Loop(); return nil },
		"brief-after-dialog": cmdBriefAfterDialog,
	}
}

func strsHas(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
