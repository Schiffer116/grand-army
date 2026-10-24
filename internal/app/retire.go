package app

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"berthier/internal/core"
	"berthier/internal/harness"
)

// --- corps-retire, corps-reopen

// retiredRec is a retired Corps in state/retired.json: where it lives and the
// session of its Marshal, for the report a reopen rebuilds from.
type retiredRec struct {
	Dir     string `json:"dir"`
	Session string `json:"session,omitempty"`
	At      int64  `json:"at"`
}

func (e *Env) retiredFile() string { return filepath.Join(e.Home, "retired.json") }

// ReadRetired is state/retired.json: {corps: record}. Its Corps stay in boards.json
// (their Board card) and Notes; the Main Loop sees nothing of them.
func (e *Env) ReadRetired() map[string]retiredRec {
	m := map[string]retiredRec{}
	readJSON(e.retiredFile(), &m)
	return m
}

// RetiredNames are the retired Corps, sorted.
func (e *Env) RetiredNames() []string { return slices.Sorted(maps.Keys(e.ReadRetired())) }

func (e *Env) setRetired(name string, r *retiredRec) error {
	m := e.ReadRetired()
	if r == nil {
		delete(m, name)
	} else {
		m[name] = *r
	}
	return writeJSONAtomic(e.retiredFile(), m, true)
}

// retiredMsg is the refusal for a message to a retired Corps.
func retiredMsg(corps string) string {
	return fmt.Sprintf("Corps '%s' is retired; the user reopens it with `berthier corps-reopen %s`", corps, corps)
}

// blockers are the reasons a Corps in dir cannot retire without losing sight
// of work: Divisions still open, uncommitted changes, commits not pushed. A
// directory that is not a git repository has no changes to lose; a repo
// with no remote has nowhere to push to.
func (e *Env) blockers(corps, dir string) []string {
	var why []string
	var open []string
	divs := e.ReadDivisions()
	for _, k := range slices.Sorted(maps.Keys(divs)) {
		if core.CorpsName(divs[k].Corps) == corps {
			open = append(open, k)
		}
	}
	if len(open) > 0 {
		why = append(why, "open Divisions: "+strings.Join(open, ", "))
	}
	git := func(args ...string) (string, bool) {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
		return strings.TrimSpace(string(out)), err == nil
	}
	if _, ok := git("rev-parse", "--git-dir"); !ok {
		return why
	}
	if out, ok := git("status", "--porcelain"); !ok {
		why = append(why, "git status failed in "+dir)
	} else if out != "" {
		why = append(why, fmt.Sprintf("dirty tree: %d changed paths in %s", len(strings.Split(out, "\n")), dir))
	}
	count := ""
	if _, ok := git("rev-parse", "--abbrev-ref", "@{u}"); ok {
		count, _ = git("rev-list", "--count", "@{u}..HEAD")
	} else if remotes, _ := git("remote"); remotes != "" {
		count, _ = git("rev-list", "--count", "HEAD", "--not", "--remotes")
	}
	if n, _ := strconv.Atoi(count); n > 0 {
		why = append(why, fmt.Sprintf("%d unpushed commits in %s", n, dir))
	}
	return why
}

// cmdCorpsRetire is the user's: stop a Corps's Marshal, close its
// workspace and leave it out of the Main Loop, the Console, Shutdown/Wake and
// asks until `corps-reopen`. Its record, Notes and Board card stay.
func cmdCorpsRetire(e *Env, args []string, io IO) error {
	if len(args) != 1 {
		return die("usage: berthier corps-retire <corps>")
	}
	name := core.CorpsName(args[0])
	if _, ok := e.ReadRetired()[name]; ok {
		return die("Corps '%s' is already retired", name)
	}
	ws, err := e.Workspaces()
	if err != nil {
		return err
	}
	var w *core.Workspace
	for i := range ws {
		if core.CorpsName(ws[i].Label) == name {
			w = &ws[i]
		}
	}
	st := e.ReadState()
	switch {
	case name == "berthier" || w == nil:
		return die("no Corps '%s' to retire; known: %s", name, knownList(e.corpsNames(false)))
	case e.ReadDivisions().IsDivision(name):
		return die("'%s' is a Division, not a Corps", name)
	case st.Berthier != nil && st.Agents.Get(*st.Berthier) != nil && st.Agents.Get(*st.Berthier).WorkspaceID == w.ID:
		return die("'%s' is Berthier's own workspace", name)
	}
	panes, err := e.Host.Panes(w.ID)
	if err != nil || len(panes) == 0 || panes[0].Cwd == nil {
		return die("no pane with a directory in Corps '%s'", name)
	}
	dir := *panes[0].Cwd
	if why := e.blockers(name, dir); len(why) > 0 {
		return die("will not retire '%s': %s", name, strings.Join(why, "; "))
	}
	session := ""
	for _, k := range st.Agents.Keys {
		if a := st.Agents.M[k]; a.WorkspaceID == w.ID && a.SessionID != nil {
			session = *a.SessionID
		}
	}
	// Marked first, so the Main Loop does not Revive the Agent we stop.
	if err := e.setRetired(name, &retiredRec{dir, session, e.now()}); err != nil {
		return err
	}
	for _, p := range panes {
		if pr, err := e.Host.Process(p.PaneID); err == nil && pr.Alive() {
			for _, k := range e.harness().Keys(harness.End) {
				e.Host.Key(p.PaneID, k)
				time.Sleep(300 * time.Millisecond)
			}
			for deadline := time.Now().Add(10 * time.Second); !e.paneAtShell(p.PaneID) && time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
			}
		}
	}
	if err := e.Host.CloseWorkspace(w.ID); err != nil {
		e.setRetired(name, nil)
		return die("could not close the workspace of '%s': %v", name, err)
	}
	e.Event("retired", "retired "+name, core.Fields{"corps": name, "why": "retired"})
	fmt.Fprintf(io.Out, "corps '%s' retired: Marshal stopped, workspace closed; `berthier corps-reopen %s` brings it back\n", name, name)
	return nil
}

// cmdCorpsReopen brings a retired Corps back: a workspace in its directory
// with a Marshal that rebuilds from Notes, Board and git, as in a Fresh Start.
func cmdCorpsReopen(e *Env, args []string, io IO) error {
	if len(args) != 1 {
		return die("usage: berthier corps-reopen <corps>")
	}
	name := core.CorpsName(args[0])
	rec, ok := e.ReadRetired()[name]
	if !ok {
		return die("Corps '%s' is not retired; retired: %s", name, knownList(e.RetiredNames()))
	}
	if ok, _ := e.hasWorkspace(name); ok {
		return die("a workspace labelled '%s' already exists", name)
	}
	if st, err := os.Stat(rec.Dir); err != nil || !st.IsDir() {
		return die("directory %s of '%s' is gone", rec.Dir, name)
	}
	created, err := e.Host.CreateWorkspace(rec.Dir, name)
	if err != nil {
		return die("could not create workspace '%s'", name)
	}
	if err := e.setRetired(name, nil); err != nil {
		return err
	}
	pane, agent := created.PaneID, core.Slug(name)
	report := e.ReadReports()[rec.Session].Line
	var warn string
	switch e.startClaude(agent, pane, core.FreshPrompt(report, ""), name, e.stickyModel(name, rec.Session), func(s string) { warn = s }) {
	case "ready":
		fmt.Fprintf(io.Out, "corps '%s' reopened: pane %s, Marshal '%s' rebuilding from Notes, Board and git\n", name, pane, agent)
	case "blocked":
		fmt.Fprintf(io.Out, "corps '%s' reopened: pane %s, Marshal '%s' is waiting on a startup dialog - the user must answer it in Herdr\n", name, pane, agent)
	default:
		return die("corps '%s' reopened (pane %s) but its Marshal did not start: %s", name, pane, warn)
	}
	e.Event("reopened", "reopened "+name, core.Fields{"corps": name, "why": "reopened"})
	return nil
}
