package core

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// State is the Grand Army State (state/state.json), written only by Tick.
type State struct {
	Version           int                  `json:"version"`
	UpdatedAt         int64                `json:"updated_at"`
	Berthier          *string              `json:"berthier"`
	Corps             []Corps              `json:"corps"`
	BerthierDivisions []UnitState          `json:"berthier_divisions"`
	Order             []string             `json:"order"`
	Agents            Agents               `json:"agents"`
	Escalated         map[string]int64     `json:"escalated"`
	Interrupted       map[string]Interrupt `json:"interrupted"`
	AskLog            []AskLogEntry        `json:"ask_log"`
}

// Interrupt remembers an Agent that was working before a restart gap, by
// session, until it reappears.
type Interrupt struct {
	Name  *string `json:"name"`
	Since int64   `json:"since"`
	Tries int     `json:"tries,omitempty"` // continue nudges sent so far
}

// Corps is a workspace that is neither a Division nor Berthier's own.
type Corps struct {
	Name        string      `json:"name"`
	WorkspaceID string      `json:"workspace_id"`
	RepoRoot    *string     `json:"repo_root"`
	Agents      []string    `json:"agents"`
	Divisions   []UnitState `json:"divisions"`
}

// UnitState is a Division in the state: its workspace, where it works, its Agents.
type UnitState struct {
	Name        string   `json:"name"`
	WorkspaceID string   `json:"workspace_id"`
	Worktree    bool     `json:"worktree"`
	Path        *string  `json:"path"`
	Agents      []string `json:"agents"`
}

// AskLogEntry remembers a delivered ask for the hour's ping-pong count.
type AskLogEntry struct {
	From string `json:"from"`
	To   string `json:"to"`
	At   int64  `json:"at"`
}

// Limit is the reading of one idle episode for a usage-limit stop.
type Limit struct {
	Episode string `json:"episode"`
	Since   int64  `json:"since"`
	ResetAt *int64 `json:"reset_at"`
	Resumed bool   `json:"resumed,omitempty"`
}

// Agent is what the Main Loop remembers of one Agent.
type Agent struct {
	Name           *string `json:"name"`
	Kind           *string `json:"kind"`
	Status         string  `json:"status"`
	StatusSince    int64   `json:"status_since"`
	PrevStatus     *string `json:"prev_status"`
	SessionID      *string `json:"session_id"`
	WorkspaceID    string  `json:"workspace_id"`
	WorkspaceLabel *string `json:"workspace_label"`
	TabID          *string `json:"tab_id"`
	Cwd            *string `json:"cwd"`
	Title          *string `json:"title"`
	// Herdr bumps StateSeq on every status change, even a turn between two ticks.
	StateSeq       *int64 `json:"state_seq"`
	Limit          *Limit `json:"limit"`
	LimitResumedAt *int64 `json:"limit_resumed_at"`
	ReviveAttempts int    `json:"revive_attempts"`
	LastReviveAt   *int64 `json:"last_revive_at"`
	// Last time its input box was read holding the user's draft.
	DraftAt *int64 `json:"draft_at,omitempty"`
	// Due a Revive or a usage-limit resume with a cold cache: held for the user.
	Frozen *Frozen `json:"frozen,omitempty"`
}

// Agents is an object of Agents by pane id that keeps insertion order, which
// the views (and so the Console) show.
type Agents struct {
	Keys []string
	M    map[string]*Agent
}

func (a *Agents) Get(k string) *Agent {
	if a == nil {
		return nil
	}
	return a.M[k]
}

func (a *Agents) Set(k string, v *Agent) {
	if a.M == nil {
		a.M = map[string]*Agent{}
	}
	if _, ok := a.M[k]; !ok {
		a.Keys = append(a.Keys, k)
	}
	a.M[k] = v
}

func (a *Agents) Delete(k string) {
	if _, ok := a.M[k]; !ok {
		return
	}
	delete(a.M, k)
	for i, x := range a.Keys {
		if x == k {
			a.Keys = append(a.Keys[:i:i], a.Keys[i+1:]...)
			break
		}
	}
}

func (a Agents) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range a.Keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, err := json.Marshal(a.M[k])
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func (a *Agents) UnmarshalJSON(data []byte) error {
	*a = Agents{}
	dec := json.NewDecoder(bytes.NewReader(data))
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if t == nil { // null
		return nil
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("agents: want an object")
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		var v Agent
		if err := dec.Decode(&v); err != nil {
			return err
		}
		a.Set(kt.(string), &v)
	}
	return nil
}
