package core

import (
	"fmt"
	"slices"
)

// BoardDoc is a Skipli board as `skipli-cli board <id> --json` reads it.
type BoardDoc struct {
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Cards []BoardCard `json:"cards"`
}

type BoardCard struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Tasks       []BoardTask `json:"tasks"`
}

// NotesCard names the card at the top of a Corps's own Board whose
// description holds the Corps's Notes (a Corps on the Grand Army Board keeps
// them in its card's description). It is not work: never Board news.
const NotesCard = "notes"

type BoardTask struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// BoardBigTasks: a Corps's card on the Grand Army Board with more pending
// tasks than this is a suggestion to give the Corps its own Board.
const BoardBigTasks = 10

// BoardNew is a card or task the Marshal of Corps has not been told of.
type BoardNew struct{ Corps, Key, Text string }

// BoardNews compares a board with the ids already seen on it. card is the
// Corps's card on the Grand Army Board ("" for a Corps with its own Board:
// every new card and task counts). seen == nil means the board was never
// polled since it was adopted: the poll only seeds, nothing is news. The
// second result is the seen set to keep (every id on the board, so a task
// moved into the card is not news). Edits, moves and deletes never are.
func BoardNews(doc BoardDoc, corps, card string, seen []string) ([]BoardNew, []string) {
	known := map[string]bool{}
	for _, id := range seen {
		known[id] = true
	}
	var news []BoardNew
	keep := append([]string(nil), seen...)
	note := func(id string) bool {
		if known[id] {
			return false
		}
		known[id] = true
		keep = append(keep, id)
		return seen != nil
	}
	for _, c := range doc.Cards {
		if card == "" && c.Name == NotesCard {
			note(c.ID)
			for _, t := range c.Tasks {
				note(t.ID)
			}
			continue
		}
		if note(c.ID) && card == "" {
			news = append(news, BoardNew{corps, c.ID, fmt.Sprintf("[board] new card %s %q on %s", c.ID, c.Name, doc.Name)})
		}
		for _, t := range c.Tasks {
			if note(t.ID) && (card == "" || card == c.ID) {
				news = append(news, BoardNew{corps, t.ID, fmt.Sprintf("[board] new task %s %q on %s", t.ID, t.Name, doc.Name)})
			}
		}
	}
	if keep == nil {
		keep = []string{}
	}
	slices.Sort(keep)
	return news, keep
}

// BoardPending counts the pending tasks of a card.
func BoardPending(doc BoardDoc, card string) (n int) {
	for _, c := range doc.Cards {
		if c.ID == card {
			for _, t := range c.Tasks {
				if t.Status != "done" {
					n++
				}
			}
		}
	}
	return n
}

// BoardCardNamed finds a card by name.
func BoardCardNamed(doc BoardDoc, name string) (string, bool) {
	for _, c := range doc.Cards {
		if c.Name == name {
			return c.ID, true
		}
	}
	return "", false
}
