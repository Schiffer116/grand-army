// Package agentsim plays Claude-like Agents in the panes of a hosttest.Fake,
// for tests of everything above the Host: how an Agent takes a prompt, what
// its screen shows, what its state does. It speaks claudetest's screen format.
//
// Mode says how an Agent takes a prompt:
//
//	takes   submits at once          draft   holds it until a second Enter (image paths)
//	lost    drops the first paste    busy    working: queues it, no state change
//	never   holds it whatever        error   the Host refuses the text
//	typing  the user's draft sits in the box
//	nostart the Host refuses to start it
//	dialog  it starts behind a dialog (status blocked) until Answer
//	noclear /clear does not start a new session
package agentsim

import (
	"errors"
	"fmt"
	"sync"

	"berthier/internal/host"
	"berthier/internal/host/hosttest"
)

// Sim is the Agents of one Fake.
type Sim struct {
	F    *hosttest.Fake
	Mode string

	mu     sync.Mutex
	agents map[string]*agent
}

type agent struct {
	box           string
	typed, enters int
	sid           string
	seq           int64
	status        string
	lastText      string // what the last submit took
}

// New installs the Sim into f's hooks.
func New(f *hosttest.Fake, mode string) *Sim {
	s := &Sim{F: f, Mode: mode, agents: map[string]*agent{}}
	f.OnType = s.onType
	f.OnKey = s.onKey
	f.OnStart = func(pane string, argv []string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		a := &agent{sid: "s1", seq: 1, status: "idle"}
		if mode == "dialog" {
			a.status = "blocked"
		}
		s.agents[pane] = a
		s.sync(pane)
	}
	f.Fail = func(method string) error {
		switch {
		case mode == "error" && method == "Type":
			return errors.New("agent target x not found")
		case mode == "nostart" && method == "Start":
			return errors.New("agent arguments cannot be encoded safely for the target shell")
		}
		return nil
	}
	return s
}

// Attach makes the pane run an Agent already (idle, session s1).
func (s *Sim) Attach(pane string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := &agent{sid: "s1", seq: 1, status: "idle"}
	switch s.Mode {
	case "busy":
		a.status = "working"
	case "typing":
		a.box = "half a thought"
	case "dialog":
		a.status = "blocked"
	}
	s.agents[pane] = a
	s.sync(pane)
}

// Answer dismisses the startup dialog of the Agent in pane.
func (s *Sim) Answer(pane string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.agents[pane]; a != nil {
		a.status = "idle"
		s.sync(pane)
	}
}

// Took is the text the Agent in pane last took (submitted or queued).
func (s *Sim) Took(pane string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.agents[pane]; a != nil {
		return a.lastText
	}
	return ""
}

// Session is the session id of the Agent in pane.
func (s *Sim) Session(pane string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.agents[pane]; a != nil {
		return a.sid
	}
	return ""
}

func (s *Sim) onType(pane, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agents[pane]
	if a == nil {
		return
	}
	// The Fake echoes typed text on its input line; the Sim owns the box.
	a.typed++
	if !(s.Mode == "lost" && a.typed == 1) {
		a.box += text
	}
	s.sync(pane)
}

func (s *Sim) onKey(pane string, k host.Key) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agents[pane]
	if a == nil {
		return
	}
	switch k {
	case host.KeyEnter:
		s.enter(a)
	case host.KeyCtrlU:
		a.box = ""
	}
	s.sync(pane)
}

// enter is Enter in the box.
func (s *Sim) enter(a *agent) {
	if a.box == "" || s.Mode == "never" || s.Mode == "typing" {
		return
	}
	a.enters++
	switch s.Mode {
	case "draft":
		if a.enters == 1 {
			return
		}
	case "busy":
		a.lastText, a.box = a.box, "" // queued: no state change
		return
	}
	a.lastText = a.box
	if a.box == "/clear" && s.Mode != "noclear" {
		a.sid = fmt.Sprintf("s%d", a.seq+1)
	}
	a.seq++
	a.box = ""
}

// sync publishes the Agent's state: its snapshot entry and its screen.
func (s *Sim) sync(pane string) {
	a := s.agents[pane]
	s.F.Update(pane, func(p *hosttest.Pane) {
		p.Proc = host.Process{Name: "claude"}
		p.Input = ""
		p.Lines = []string{"history", "box: " + a.box}
		seq, sid, claude := a.seq, a.sid, "claude"
		p.Agent, p.Status, p.StateSeq, p.Session = &claude, a.status, &seq, &sid
	})
}
