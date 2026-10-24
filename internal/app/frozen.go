package app

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"berthier/internal/core"
)

// Frozen Agents: the user's choice (`revive`, `fresh`, the Console) is a file
// in state/thaw/ that core.Tick decides on its next pass, like the inbox.

// ReadThaws lists the user's pending choices for Frozen Agents.
func (e *Env) ReadThaws() []core.Thaw {
	l := []core.Thaw{}
	for _, f := range jsonFiles(e.Thaws) {
		var t core.Thaw
		if readJSON(f, &t) {
			t.ID = fileID(filepath.Base(f))
			l = append(l, t)
		}
	}
	return l
}

// Thaw records the user's choice for a Frozen Agent: how is "revive" or
// "fresh". A later choice for the same pane replaces an undecided one.
func (e *Env) Thaw(pane, how string) error {
	if how != "revive" && how != "fresh" {
		return badRequest("want revive or fresh")
	}
	st := e.ReadState()
	a := st.Agents.Get(pane)
	if a == nil || a.Frozen == nil {
		return badRequest(fmt.Sprintf("%s is not a Frozen Agent", pane))
	}
	b, _ := json.Marshal(core.Thaw{Pane: pane, How: how, At: e.now()})
	return writeAtomic(e.thawFile(pane), append(b, '\n'))
}

// badRequest is a refusal caused by the request (the Console answers 400).
type badRequest string

func (b badRequest) Error() string { return string(b) }
func (badRequest) Bad() bool       { return true }

// thawAndTick is the Console's choice: recorded, then decided at once by a
// tick of its own (the loop's next one, if a tick is running).
func (e *Env) thawAndTick(pane, how string) error {
	if err := e.Thaw(pane, how); err != nil {
		return err
	}
	go e.Tick()
	return nil
}

func (e *Env) thawFile(pane string) string {
	return filepath.Join(e.Thaws, strings.NewReplacer(":", "_", "/", "_").Replace(pane)+".json")
}

// cmdThaw is `revive <pane>` and `fresh <pane>`: record the choice, decide it
// with a tick of our own (or the loop's, if one is running), and print what
// the Main Loop did.
func cmdThaw(how string) command {
	return func(e *Env, args []string, io IO) error {
		if len(args) != 1 {
			return die("usage: berthier %s <pane>", how)
		}
		pane := args[0]
		if err := e.Thaw(pane, how); err != nil {
			return err
		}
		mark := int64(0)
		if st, err := os.Stat(e.Log); err == nil {
			mark = st.Size()
		}
		if err := e.Tick(); err != nil {
			return err
		}
		for deadline := time.Now().Add(90 * time.Second); ; time.Sleep(500 * time.Millisecond) {
			if _, err := os.Stat(e.thawFile(pane)); os.IsNotExist(err) {
				break
			}
			if time.Now().After(deadline) {
				return die("%s requested for %s, not decided yet (is the user's draft in its box?); see berthier log", how, pane)
			}
		}
		said := false
		for _, l := range linesSince(e.Log, mark) {
			var ev struct{ Kind, Pane, Msg string }
			if json.Unmarshal([]byte(l), &ev) == nil && ev.Pane == pane && slices.Contains(thawOutcomes, ev.Kind) {
				fmt.Fprintln(io.Out, ev.Msg)
				said = true
			}
		}
		if !said {
			fmt.Fprintf(io.Out, "%s of %s decided; see berthier log\n", how, pane)
		}
		return nil
	}
}

var thawOutcomes = []string{"revived", "revive_failed", "resumed", "resume_failed", "fresh_started", "fresh_failed"}

func linesSince(path string, offset int64) []string {
	b, err := os.ReadFile(path)
	if err != nil || int64(len(b)) < offset {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b[offset:])), "\n")
}

// freshStart replaces a Frozen Agent's session with a new one in the same
// pane, on the same model, told to rebuild from its Corps's Notes, its Board,
// its last report and git; a Division also gets its Brief again.
func (e *Env) freshStart(f core.FreshStart) {
	fail := func(why string) {
		e.Event("fresh_failed", fmt.Sprintf("Fresh Start failed: %s in %s: %s", f.Name, f.PaneID, why), with(f.Ev, "why", why))
		e.H.Run("notification", "show", "Fresh Start failed", "--body", fmt.Sprintf("%s (%s): %s", f.Name, f.PaneID, why), "--sound", "request")
	}
	if f.Live {
		// Claude stopped on its usage limit: two Ctrl-C end it, back to the shell.
		for i := 0; i < 2; i++ {
			e.H.Run("pane", "send-keys", f.PaneID, "ctrl+c")
			time.Sleep(300 * time.Millisecond)
		}
		for deadline := time.Now().Add(15 * time.Second); !e.paneAtShell(f.PaneID) && time.Now().Before(deadline); {
			time.Sleep(300 * time.Millisecond)
		}
	}
	if !e.paneAtShell(f.PaneID) {
		fail("its pane is not at a shell prompt")
		return
	}
	report := e.ReadReports()[f.Session].Line
	brief := ""
	model := f.Model
	if f.Division != "" {
		if b, err := os.ReadFile(e.briefFile(f.Division)); err == nil {
			brief = strings.TrimSpace(string(b))
		}
		if model == "" {
			model = e.ModelOf(f.Division)
		}
	}
	var warn string
	switch e.startClaude(f.Name, f.PaneID, core.FreshPrompt(report, brief), f.Label, model, func(s string) { warn = s }) {
	case "ready":
		e.Event("fresh_started", fmt.Sprintf("Fresh Start: %s in %s, a new session on %s", f.Name, f.PaneID, cmp.Or(model, "claude's default model")), f.Ev)
	case "blocked":
		e.Event("fresh_started", fmt.Sprintf("Fresh Start: %s in %s waits on a startup dialog; its prompt follows once the user answers", f.Name, f.PaneID), f.Ev)
	default:
		fail(cmp.Or(warn, "Claude did not start"))
	}
}

// A spawned Division's Brief, kept for a Fresh Start.
func (e *Env) briefFile(division string) string { return filepath.Join(e.Briefs, division+".md") }

func (e *Env) saveBrief(division, brief string) error {
	if err := os.MkdirAll(e.Briefs, 0o755); err != nil {
		return err
	}
	return writeAtomic(e.briefFile(division), []byte(brief+"\n"))
}

// autoCheck reads what an automatic Fresh Start of a Frozen Division needs:
// its Brief on file and its worktree's `git status --porcelain`.
func (e *Env) autoCheck(pane string) *core.AutoCheck {
	c := &core.AutoCheck{}
	st := e.ReadState()
	a := st.Agents.Get(pane)
	if a == nil {
		c.Err = "agent unknown"
		return c
	}
	name := core.CorpsName(strOr(a.WorkspaceLabel))
	_, err := os.Stat(e.briefFile(name))
	c.Brief = err == nil
	out, err := exec.Command("git", "-C", strOr(a.Cwd), "status", "--porcelain").Output()
	if err != nil {
		c.Err = err.Error()
	}
	c.Dirty = string(out)
	return c
}

// wake delivers a queued wake: /clear the Agent's context (confirmed by its
// new session), then the wake prompt with what it needs to rebuild, read before
// the clear. A failed clear sends nothing more; the ask stays queued for the next tick.
func (e *Env) wake(d core.Delivery) {
	name, report, brief := "", "", ""
	st := e.ReadState()
	if a := st.Agents.Get(d.PaneID); a != nil {
		name = strOr(a.Name)
		report = e.ReadReports()[strOr(a.SessionID)].Line
		if b, err := os.ReadFile(e.briefFile(core.CorpsName(strOr(a.WorkspaceLabel)))); err == nil {
			brief = strings.TrimSpace(string(b))
		}
	}
	if err := e.H.ClearAgent(d.PaneID); err != nil {
		e.Event("wake_clear_failed", fmt.Sprintf("wake: /clear failed: %s -> %s (%s), ask %s: %s", d.From, d.To, d.PaneID, d.ID, err), with(d.Ev, "why", err.Error()))
		return
	}
	if name != "" {
		// Herdr keeps a pane's name across /clear; make sure.
		e.H.Run("agent", "rename", d.PaneID, name)
	}
	if err := e.H.PromptAgent(d.PaneID, d.Text+core.Rebuild(report, brief)); err != nil {
		e.Event("delivery_failed", fmt.Sprintf("wake delivery failed after /clear: %s (%s), ask %s: %s", d.To, d.PaneID, d.ID, err), with(d.Ev, "why", err.Error()))
		return
	}
	os.Remove(filepath.Join(e.Inbox, d.ID+".json"))
	e.Event("delivered", fmt.Sprintf("woke %s (%s): context cleared, wake prompt sent", d.To, d.PaneID), d.Ev)
}
