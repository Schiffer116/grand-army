// Package hosttest is the Go fake of the Host port and the contract suite both
// it and the real adapter must pass.
package hosttest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"berthier/internal/core"
	"berthier/internal/host"
)

// Call is one call on the Fake, in order.
type Call struct {
	Method string
	Args   []string
}

func (c Call) String() string { return c.Method + " " + strings.Join(c.Args, " ") }

// Pane is the Fake's state of a pane: the snapshot entry, a terminal of
// lines with an input line, and the foreground process. It behaves like a
// shell that knows `echo`; tests script an Agent on top with OnType and OnKey
// and Update.
type Pane struct {
	core.SnapAgent
	Lines []string     // terminal lines above the input line
	Input string       // the typed, unsubmitted text
	Proc  host.Process // the foreground process
	cmd   *exec.Cmd    // a real process, when the Fake Execs
}

// Fake is an in-memory Host.
type Fake struct {
	// Down makes every call fail with host.ErrUnreachable.
	Down bool
	// Exec makes Start really run the process, with the pane and workspace in
	// its environment (FAKE_HOST_PANE, FAKE_HOST_WORKSPACE); a key press of
	// interrupt kills it. Without it Start only records the call and the pane's
	// foreground becomes the process until a test or an interrupt ends it.
	Exec bool
	// Fail, when set, makes a call fail with the error it returns for the
	// method's name (nil: the call goes ahead).
	Fail func(method string) error
	// OnType and OnKey run after the default effect, without the lock held,
	// so a test can play the program in a pane.
	OnType  func(pane, text string)
	OnKey   func(pane string, k host.Key)
	OnStart func(pane string, argv []string)

	mu         sync.Mutex
	workspaces []core.Workspace
	panes      map[string]*Pane
	order      []string
	calls      []Call
	notes      [][2]string
	nWS        int
}

// New is an empty Fake.
func New() *Fake { return &Fake{panes: map[string]*Pane{}} }

var _ host.Host = (*Fake)(nil)

func (f *Fake) record(method string, args ...string) {
	f.calls = append(f.calls, Call{method, args})
}

// Calls are the calls so far, oldest first.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// CallsTo are the calls of one method.
func (f *Fake) CallsTo(method string) []Call {
	var out []Call
	for _, c := range f.Calls() {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// Notifications are the {title, body} pairs shown so far.
func (f *Fake) Notifications() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]string(nil), f.notes...)
}

// Update changes a pane under the lock; false when there is none.
func (f *Fake) Update(pane string, fn func(*Pane)) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.panes[pane]
	if p == nil {
		return false
	}
	fn(p)
	return true
}

// AddPane adds a pane to an existing workspace, as a split would.
func (f *Fake) AddPane(workspace string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.newPane(workspace, "")
}

func (f *Fake) newPane(workspace, cwd string) string {
	n := 1
	for _, p := range f.panes {
		if p.WorkspaceID == workspace {
			n++
		}
	}
	id := fmt.Sprintf("%s:p%d", workspace, n)
	p := &Pane{SnapAgent: core.SnapAgent{PaneID: id, WorkspaceID: workspace, Status: "unknown"}, Proc: host.Process{Name: "sh", AtShell: true}}
	if cwd != "" {
		p.Cwd = &cwd
	}
	f.panes[id] = p
	f.order = append(f.order, id)
	return id
}

func (f *Fake) newWorkspace(cwd, label string, wt *core.Worktree) host.Created {
	f.nWS++
	id := fmt.Sprintf("w%d", f.nWS)
	f.workspaces = append(f.workspaces, core.Workspace{ID: id, Label: label, Number: f.nWS, Worktree: wt})
	return host.Created{WorkspaceID: id, PaneID: f.newPane(id, cwd)}
}

func (f *Fake) ws(id string) (int, error) {
	for i, w := range f.workspaces {
		if w.ID == id {
			return i, nil
		}
	}
	return -1, fmt.Errorf("workspace %s: %w", id, host.ErrNotFound)
}

func (f *Fake) pane(id string) (*Pane, error) {
	if p := f.panes[id]; p != nil {
		return p, nil
	}
	return nil, fmt.Errorf("pane %s: %w", id, host.ErrNotFound)
}

func (f *Fake) enter(method string, args ...string) error {
	f.record(method, args...)
	if f.Down {
		return host.ErrUnreachable
	}
	if f.Fail != nil {
		return f.Fail(method)
	}
	return nil
}

func (f *Fake) Snapshot() (core.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Snapshot"); err != nil {
		return core.Snapshot{}, err
	}
	s := core.Snapshot{Workspaces: append([]core.Workspace(nil), f.workspaces...)}
	for _, id := range f.order {
		p := f.panes[id].SnapAgent
		s.Panes = append(s.Panes, p)
		if p.Agent != nil {
			s.Agents = append(s.Agents, p)
		}
	}
	return s, nil
}

func (f *Fake) Workspaces() ([]core.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Workspaces"); err != nil {
		return nil, err
	}
	return append([]core.Workspace(nil), f.workspaces...), nil
}

func (f *Fake) Workspace(id string) (core.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Workspace", id); err != nil {
		return core.Workspace{}, err
	}
	i, err := f.ws(id)
	if err != nil {
		return core.Workspace{}, err
	}
	return f.workspaces[i], nil
}

func (f *Fake) CreateWorkspace(cwd, label string) (host.Created, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CreateWorkspace", cwd, label); err != nil {
		return host.Created{}, err
	}
	return f.newWorkspace(cwd, label, nil), nil
}

func (f *Fake) CloseWorkspace(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CloseWorkspace", id); err != nil {
		return err
	}
	i, err := f.ws(id)
	if err != nil {
		return err
	}
	f.workspaces = append(f.workspaces[:i:i], f.workspaces[i+1:]...)
	var keep []string
	for _, pid := range f.order {
		if p := f.panes[pid]; p.WorkspaceID == id {
			p.kill()
			delete(f.panes, pid)
		} else {
			keep = append(keep, pid)
		}
	}
	f.order = keep
	return nil
}

func (f *Fake) RenameWorkspace(id, label string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("RenameWorkspace", id, label); err != nil {
		return err
	}
	i, err := f.ws(id)
	if err != nil {
		return err
	}
	f.workspaces[i].Label = label
	return nil
}

// checkout is where the Fake puts the worktree of branch of repo.
func checkout(repo, branch string) string {
	return filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-worktrees", branch)
}

func (f *Fake) CreateWorktree(repo, branch, label string) (host.Created, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CreateWorktree", repo, branch, label); err != nil {
		return host.Created{}, err
	}
	path := checkout(repo, branch)
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", branch, path).CombinedOutput(); err != nil {
		return host.Created{}, fmt.Errorf("git worktree add: %s", strings.TrimSpace(string(out)))
	}
	return f.newWorkspace(path, label, &core.Worktree{RepoRoot: &repo, CheckoutPath: &path}), nil
}

func (f *Fake) RemoveWorktree(workspace string) error {
	f.mu.Lock()
	i, err := f.ws(workspace)
	if err == nil {
		err = f.enter("RemoveWorktree", workspace)
	}
	if err != nil {
		f.mu.Unlock()
		return err
	}
	wt := f.workspaces[i].Worktree
	f.mu.Unlock()
	if wt == nil || wt.CheckoutPath == nil || wt.RepoRoot == nil {
		return fmt.Errorf("workspace %s is not a worktree", workspace)
	}
	if out, _ := exec.Command("git", "-C", *wt.CheckoutPath, "status", "--porcelain").Output(); len(out) > 0 {
		return fmt.Errorf("%s has uncommitted changes", *wt.CheckoutPath)
	}
	if out, err := exec.Command("git", "-C", *wt.RepoRoot, "worktree", "remove", *wt.CheckoutPath).CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove: %s", strings.TrimSpace(string(out)))
	}
	return f.CloseWorkspace(workspace)
}

func (f *Fake) Panes(workspace string) ([]core.SnapAgent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Panes", workspace); err != nil {
		return nil, err
	}
	if workspace != "" {
		if _, err := f.ws(workspace); err != nil {
			return nil, err
		}
	}
	var out []core.SnapAgent
	for _, id := range f.order {
		if p := f.panes[id]; workspace == "" || p.WorkspaceID == workspace {
			out = append(out, p.SnapAgent)
		}
	}
	return out, nil
}

func (f *Fake) Pane(id string) (core.SnapAgent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Pane", id); err != nil {
		return core.SnapAgent{}, err
	}
	p, err := f.pane(id)
	if err != nil {
		return core.SnapAgent{}, err
	}
	return p.SnapAgent, nil
}

// screenRows is the Fake's visible screen height.
const screenRows = 24

func (f *Fake) Read(pane string, o host.ReadOpts) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Read", pane, fmt.Sprintf("recent=%d ansi=%v", o.Recent, o.ANSI)); err != nil {
		return "", err
	}
	p, err := f.pane(pane)
	if err != nil {
		return "", err
	}
	lines := append(append([]string(nil), p.Lines...), p.Input)
	if p.Proc.AtShell {
		lines[len(lines)-1] = "$ " + p.Input
	}
	n := screenRows
	if o.Recent > 0 {
		n = o.Recent
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

func (f *Fake) Type(pane, text string) error {
	f.mu.Lock()
	if err := f.enter("Type", pane, text); err != nil {
		f.mu.Unlock()
		return err
	}
	p, err := f.pane(pane)
	if err != nil {
		f.mu.Unlock()
		return err
	}
	p.Input += text
	hook := f.OnType
	f.mu.Unlock()
	if hook != nil {
		hook(pane, text)
	}
	return nil
}

func (f *Fake) Key(pane string, k host.Key) error {
	f.mu.Lock()
	if err := f.enter("Key", pane, string(k)); err != nil {
		f.mu.Unlock()
		return err
	}
	p, err := f.pane(pane)
	if err != nil {
		f.mu.Unlock()
		return err
	}
	switch k {
	case host.KeyEnter:
		if p.Proc.AtShell {
			p.Lines = append(p.Lines, "$ "+p.Input)
			if rest, ok := strings.CutPrefix(p.Input, "echo "); ok {
				p.Lines = append(p.Lines, rest)
			}
		} else {
			p.Lines = append(p.Lines, p.Input)
		}
		p.Input = ""
	case host.KeyEscape:
	case host.KeyCtrlC:
		p.Lines = append(p.Lines, "^C")
		p.Input = ""
		if !p.Proc.AtShell {
			p.kill()
			p.Proc = host.Process{Name: "sh", AtShell: true}
		}
	case host.KeyCtrlU:
		p.Input = ""
	default:
		f.mu.Unlock()
		return fmt.Errorf("unknown key %q", k)
	}
	hook := f.OnKey
	f.mu.Unlock()
	if hook != nil {
		hook(pane, k)
	}
	return nil
}

func (f *Fake) RenamePane(pane, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("RenamePane", pane, name); err != nil {
		return err
	}
	p, err := f.pane(pane)
	if err != nil {
		return err
	}
	p.Name = &name
	return nil
}

func (f *Fake) Process(pane string) (host.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Process", pane); err != nil {
		return host.Process{}, err
	}
	p, err := f.pane(pane)
	if err != nil {
		return host.Process{}, err
	}
	return p.Proc, nil
}

func (f *Fake) Start(pane string, argv []string) error {
	err := f.start(pane, argv)
	if err == nil && f.OnStart != nil {
		f.OnStart(pane, argv)
	}
	return err
}

func (f *Fake) start(pane string, argv []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Start", append([]string{pane}, argv...)...); err != nil {
		return err
	}
	p, err := f.pane(pane)
	if err != nil {
		return err
	}
	if len(argv) == 0 {
		return fmt.Errorf("nothing to start")
	}
	if !p.Proc.AtShell {
		return fmt.Errorf("pane %s is not at a shell prompt", pane)
	}
	p.Lines = append(p.Lines, "$ "+strings.Join(argv, " "))
	p.Proc = host.Process{Name: filepath.Base(argv[0])}
	if !f.Exec {
		return nil
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "FAKE_HOST_PANE="+pane, "FAKE_HOST_WORKSPACE="+p.WorkspaceID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if p.Cwd != nil {
		cmd.Dir = *p.Cwd
	}
	if err := cmd.Start(); err != nil {
		p.Proc = host.Process{Name: "sh", AtShell: true}
		return err
	}
	p.cmd = cmd
	go func() {
		cmd.Wait()
		f.mu.Lock()
		defer f.mu.Unlock()
		if p.cmd == cmd {
			p.cmd, p.Proc = nil, host.Process{Name: "sh", AtShell: true}
		}
	}()
	return nil
}

// kill ends the pane's real process, if any.
func (p *Pane) kill() {
	if p.cmd != nil && p.cmd.Process != nil {
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
	p.cmd = nil
}

func (f *Fake) Self(getenv func(string) string) (pane, workspace string) {
	return getenv("FAKE_HOST_PANE"), getenv("FAKE_HOST_WORKSPACE")
}

func (f *Fake) Notify(title, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Notify", title, body); err != nil {
		return err
	}
	f.notes = append(f.notes, [2]string{title, body})
	return nil
}
