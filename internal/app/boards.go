package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"berthier/internal/core"
)

// grandArmyBoard is the name of the Board shared by every Corps without its own.
const grandArmyBoard = "Grand Army"

// boardRec is where a Corps's work lives: its own Board (Card empty), or its
// card on the Grand Army Board.
type boardRec struct {
	Board string `json:"board"`
	Card  string `json:"card,omitempty"`
}

// boardStore is state/boards.json, written by `board` and the Main Loop (under
// state/boards.lock), never by core.Tick.
type boardStore struct {
	Shared   string              `json:"shared,omitempty"`  // the Grand Army Board
	Corps    map[string]boardRec `json:"corps"`             // by core.CorpsName
	Seen     map[string][]string `json:"seen"`              // card and task ids seen per Board; absent: seed silently
	Failing  map[string]int64    `json:"failing,omitempty"` // Board -> when its run of failed polls began
	PolledAt int64               `json:"polled_at"`
	Alerts   map[string]bool     `json:"alerts"` // Escalations made and not yet recovered
}

func (e *Env) loadBoards() boardStore {
	var s boardStore
	readJSON(e.BoardsFile, &s)
	if s.Corps == nil {
		s.Corps = map[string]boardRec{}
	}
	if s.Seen == nil {
		s.Seen = map[string][]string{}
	}
	if s.Alerts == nil {
		s.Alerts = map[string]bool{}
	}
	return s
}

func (e *Env) saveBoards(s boardStore) error { return writeJSONAtomic(e.BoardsFile, s, true) }

// skipli runs skipli-cli on the configured Environment. Berthier never signs
// in: an error is whatever skipli-cli printed.
func (e *Env) skipli(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, e.SkipliBin, append([]string{"--env", e.SkipliEnv}, args...)...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return out.Bytes(), nil
}

func (e *Env) skipliBoard(id string) (doc core.BoardDoc, err error) {
	out, err := e.skipli("board", id, "--json")
	if err == nil {
		err = json.Unmarshal(out, &doc)
	}
	return doc, err
}

// sessionProblem: skipli-cli says to log in (never signed in, or the Session ran out).
func sessionProblem(err error) bool { return err != nil && strings.Contains(err.Error(), "login") }

// ensureSharedBoard finds or creates the Grand Army Board.
func (e *Env) ensureSharedBoard(s *boardStore) (core.BoardDoc, error) {
	if s.Shared != "" {
		if doc, err := e.skipliBoard(s.Shared); err == nil {
			return doc, nil
		}
	}
	find := func() (string, error) {
		out, err := e.skipli("boards", "--json")
		if err != nil {
			return "", err
		}
		var l struct{ Boards []struct{ ID, Name string } }
		if err := json.Unmarshal(out, &l); err != nil {
			return "", err
		}
		for _, b := range l.Boards {
			if b.Name == grandArmyBoard {
				return b.ID, nil
			}
		}
		return "", nil
	}
	id, err := find()
	if err == nil && id == "" {
		if _, err = e.skipli("new-board", grandArmyBoard); err == nil {
			id, err = find()
		}
	}
	if err != nil || id == "" {
		return core.BoardDoc{}, fmt.Errorf("no %s Board: %v", grandArmyBoard, err)
	}
	s.Shared = id
	return e.skipliBoard(id)
}

// EnsureBoard gives a Corps its Board: adopt names an existing one; otherwise
// the Corps gets a card, named after its label, on the Grand Army Board
// (created if missing). Idempotent. It returns a line for the user.
func (e *Env) EnsureBoard(label, adopt string) (string, error) {
	unlock, err := lockFile(filepath.Join(e.Home, "boards.lock"), true)
	if err != nil {
		return "", err
	}
	defer unlock()
	s := e.loadBoards()
	key := core.CorpsName(label)
	if adopt != "" {
		doc, err := e.skipliBoard(adopt)
		if err != nil {
			return "", fmt.Errorf("cannot adopt board '%s': %v", adopt, err)
		}
		s.Corps[key] = boardRec{Board: doc.ID}
		delete(s.Seen, doc.ID) // the next poll seeds, no flood
		delete(s.Alerts, "big:"+key)
		if err := e.saveBoards(s); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s works from its own Board '%s' (%s)", label, doc.Name, doc.ID), nil
	}
	if r, ok := s.Corps[key]; ok {
		return fmt.Sprintf("%s already has a Board (%s)", label, r.Board), nil
	}
	doc, err := e.ensureSharedBoard(&s)
	if err != nil {
		return "", err
	}
	card, ok := core.BoardCardNamed(doc, label)
	if !ok {
		if _, err := e.skipli("add-card", doc.ID, label); err != nil {
			return "", err
		}
		if doc, err = e.skipliBoard(doc.ID); err != nil {
			return "", err
		}
		if card, ok = core.BoardCardNamed(doc, label); !ok {
			return "", fmt.Errorf("card '%s' not found on the %s Board after adding it", label, grandArmyBoard)
		}
	}
	s.Corps[key] = boardRec{Board: doc.ID, Card: card}
	if err := e.saveBoards(s); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s works from its card (%s) on the %s Board (%s)", label, card, grandArmyBoard, doc.ID), nil
}

// pollBoards runs from the Main Loop's tick, at most every BoardPoll seconds:
// a new task in a Corps's card, or a new card or task on its own Board, goes
// to the inbox for its Main Agent (delivered like an ask: when idle, held
// while it works or its Corps is over budget). The first poll of a Board only
// seeds. Trouble (no Session, a Board that cannot be read, a card too big) is
// escalated to Berthier once, until it clears.
func (e *Env) pollBoards(out core.TickOut) {
	now := e.now()
	unlock, err := lockFile(filepath.Join(e.Home, "boards.lock"), false)
	if err != nil || unlock == nil {
		return
	}
	defer unlock()
	s := e.loadBoards()
	if len(s.Corps) == 0 || now-s.PolledAt < e.BoardPoll {
		return
	}
	s.PolledAt = now
	var esc []core.Escalation
	// once escalates a condition the first time it holds; clear re-arms it.
	once := func(key, why, text string) {
		if !s.Alerts[key] {
			s.Alerts[key] = true
			esc = append(esc, core.Escalation{Key: key, Text: text, Ev: core.Fields{"why": why, "reason": text}})
		}
	}
	byBoard := map[string][]string{}
	var boards []string
	for corps, r := range s.Corps {
		if byBoard[r.Board] == nil {
			boards = append(boards, r.Board)
		}
		byBoard[r.Board] = append(byBoard[r.Board], corps)
	}
	slices.Sort(boards)
	for _, id := range boards {
		doc, err := e.skipliBoard(id)
		if sessionProblem(err) {
			once("session", "board_session", fmt.Sprintf("Skipli Session missing or expired (%s): Boards are not polled until the user runs `skipli-cli --env %s login`; never run login yourself", err, e.SkipliEnv))
			break
		}
		if err != nil {
			// A blip is not news: escalate once it has failed poll after poll for BoardFailAfter.
			if s.Failing == nil {
				s.Failing = map[string]int64{}
			}
			if s.Failing[id] == 0 {
				s.Failing[id] = now
			}
			if now-s.Failing[id] >= e.BoardFailAfter {
				once("error:"+id, "board_error", fmt.Sprintf("Board %s not read for %dm (%s); its Corps get no new tasks until it works", id, (now-s.Failing[id])/60, err))
			}
			continue
		}
		delete(s.Failing, id)
		delete(s.Alerts, "session")
		delete(s.Alerts, "error:"+id)
		corpsOf := byBoard[id]
		slices.Sort(corpsOf)
		keep := s.Seen[id]
		for _, corps := range corpsOf {
			r := s.Corps[corps]
			card := r.Card
			if card != "" {
				if _, ok := cardIn(doc, card); !ok {
					if card, ok = core.BoardCardNamed(doc, corps); !ok {
						continue
					}
				}
			}
			news, k := core.BoardNews(doc, corps, card, s.Seen[id])
			keep = k
			for _, n := range news {
				e.queueBoardNews(n, doc.ID, now)
			}
			if card != "" {
				key := "big:" + corps
				if n := core.BoardPending(doc, card); n > core.BoardBigTasks {
					once(key, "board_big", fmt.Sprintf("%s's card on the %s Board has %d pending tasks: suggest the user gives it its own Board (they create it in Skipli, then `berthier board %s --adopt <board-id>`)", corps, grandArmyBoard, n, corps))
				} else {
					delete(s.Alerts, key)
				}
			}
		}
		s.Seen[id] = keep
	}
	e.saveBoards(s)
	if len(esc) > 0 {
		e.escalate(core.TickOut{Escalate: esc, State: out.State})
	}
}

func cardIn(doc core.BoardDoc, id string) (core.BoardCard, bool) {
	for _, c := range doc.Cards {
		if c.ID == id {
			return c, true
		}
	}
	return core.BoardCard{}, false
}

// queueBoardNews drops a one-way "board" message for a Corps's Main Agent in the inbox.
func (e *Env) queueBoardNews(n core.BoardNew, board string, now int64) {
	b, _ := json.Marshal(struct {
		Kind string `json:"kind"`
		From string `json:"from"`
		To   string `json:"to"`
		At   int64  `json:"at"`
		Text string `json:"text"`
	}{"board", "skipli", n.Corps, now, n.Text})
	if writeAtomic(filepath.Join(e.Inbox, fmt.Sprintf("board-%s-%s.json", n.Corps, n.Key)), append(b, '\n')) == nil {
		e.Event("board_news", n.Text, core.Fields{"corps": n.Corps, "board": board, "why": "board_news"})
	}
}
