package core

import (
	"reflect"
	"testing"
)

func boardDoc() BoardDoc {
	return BoardDoc{ID: "b1", Name: "Skipli", Cards: []BoardCard{
		{ID: "c1", Name: "ui", Tasks: []BoardTask{{ID: "t1", Name: "one", Status: "pending"}, {ID: "t2", Name: "two", Status: "done"}}},
		{ID: "c2", Name: "dotfiles", Tasks: []BoardTask{{ID: "t3", Name: "three", Status: "pending"}}},
	}}
}

func boardTexts(n []BoardNew) (l []string) {
	for _, x := range n {
		l = append(l, x.Corps+": "+x.Text)
	}
	return l
}

func wantTexts(t *testing.T, name string, got []BoardNew, want ...string) {
	t.Helper()
	if g := boardTexts(got); !reflect.DeepEqual(g, append([]string(nil), want...)) && !(len(g) == 0 && len(want) == 0) {
		t.Errorf("%s\n  got:  %q\n  want: %q", name, g, want)
	}
}

func TestBoardNews(t *testing.T) {
	doc := boardDoc()

	news, seen := BoardNews(doc, "skipli", "", nil)
	wantTexts(t, "first poll seeds silently", news)
	check(t, "seeded with every id", seen, `["c1","c2","t1","t2","t3"]`)
	n0, s0 := BoardNews(BoardDoc{ID: "e"}, "x", "", nil)
	wantTexts(t, "empty board seeds silently", n0)
	check(t, "empty board is seeded, not unseen", s0 != nil, `true`)

	news, _ = BoardNews(doc, "skipli", "", seen)
	wantTexts(t, "nothing new", news)

	doc.Cards[0].Tasks = append(doc.Cards[0].Tasks, BoardTask{ID: "t4", Name: "four", Status: "pending"})
	doc.Cards = append(doc.Cards, BoardCard{ID: "c3", Name: "new", Tasks: []BoardTask{{ID: "t5", Name: "five"}}})
	news, seen2 := BoardNews(doc, "skipli", "", seen)
	wantTexts(t, "own board: new task, card and its task", news,
		`skipli: [board] new task t4 "four" on Skipli`, `skipli: [board] new card c3 "new" on Skipli`, `skipli: [board] new task t5 "five" on Skipli`)

	// A Corps's card on the shared board: only new tasks in its card.
	news, _ = BoardNews(doc, "dotfiles", "c2", seen)
	wantTexts(t, "card: other cards' news ignored", news)
	doc.Cards[1].Tasks = append(doc.Cards[1].Tasks, BoardTask{ID: "t6", Name: "six"})
	news, _ = BoardNews(doc, "dotfiles", "c2", seen2)
	wantTexts(t, "card: new task", news, `dotfiles: [board] new task t6 "six" on Skipli`)

	// A task moved into the card, edited, or deleted is not news.
	moved := boardDoc()
	moved.Cards[1].Tasks = append(moved.Cards[1].Tasks, moved.Cards[0].Tasks[0])
	moved.Cards[0].Tasks = moved.Cards[0].Tasks[1:]
	moved.Cards[1].Tasks[0].Name = "renamed"
	news, _ = BoardNews(moved, "dotfiles", "c2", seen)
	wantTexts(t, "move and edit are not news", news)
	moved.Cards[1].Tasks = nil
	news, _ = BoardNews(moved, "dotfiles", "c2", seen)
	wantTexts(t, "delete is not news", news)
}

func TestBoardPending(t *testing.T) {
	doc := boardDoc()
	check(t, "pending", []any{BoardPending(doc, "c1"), BoardPending(doc, "c2"), BoardPending(doc, "nope")}, `[1,1,0]`)
	id, ok := BoardCardNamed(doc, "dotfiles")
	check(t, "card by name", []any{id, ok}, `["c2",true]`)
}

// A board message is delivered like a notify: to the Marshal when idle,
// held while it works, as it is written, never stuck, never in the ask log.
func TestBoardDelivery(t *testing.T) {
	sa := snap(W, panes(ag("w1:p1", "idle", "sid-1", "", ""), ag("w4:p1", "working", "sid-4", "", "")), panes(pn("w1:p1"), pn("w4:p1")))
	prev := tick(State{}, sa, 1000, opt{}).State
	b1 := Ask{ID: "board-skipli-t1", Kind: "board", From: "skipli", To: "skipli", Text: `[board] new task t1 "one" on Skipli`, At: 100}
	o := tick(prev, sa, 2000, opt{inbox: []Ask{b1}})
	if len(o.Deliver) != 1 || o.Deliver[0].Text != b1.Text || !o.Deliver[0].Oneway {
		t.Errorf("board message not delivered as written: %+v", o.Deliver)
	}
	check(t, "board never stuck or logged", []any{escKeys(o.Escalate), o.State.AskLog}, `[[],[]]`)
	b2 := Ask{ID: "board-notes-t2", Kind: "board", From: "skipli", To: "notes", Text: "x", At: 100}
	o = tick(prev, sa, 2000, opt{inbox: []Ask{b2}})
	check(t, "held while its Corps works or is absent", []any{len(o.Deliver), escKeys(o.Escalate)}, `[0,[]]`)
	// Berthier's pane is also the Marshal of the Corps named after its workspace.
	sb := snap(W, panes(ag("w4:p1", "idle", "sid-b", "", "/home/u/berthier")), panes(pn("w4:p1")))
	b3 := Ask{ID: "board-notes-t3", Kind: "board", From: "skipli", To: "notes", Text: "y", At: 100}
	if o = tick(State{}, sb, 2000, opt{inbox: []Ask{b3}}); len(o.Deliver) != 1 || o.Deliver[0].PaneID != "w4:p1" {
		t.Errorf("board message for the Corps of Berthier's workspace: %+v", o.Deliver)
	}
	if OriginOf(b1.Text) != "board" {
		t.Errorf("origin of a board message: %s", OriginOf(b1.Text))
	}
}

func TestNotesCardIsNotNews(t *testing.T) {
	doc := BoardDoc{ID: "b", Name: "Skipli", Cards: []BoardCard{{ID: "n", Name: NotesCard, Description: "x", Tasks: []BoardTask{{ID: "nt", Name: "t"}}}, {ID: "c", Name: "work"}}}
	news, keep := BoardNews(doc, "skipli", "", []string{})
	if len(news) != 1 || news[0].Key != "c" || len(keep) != 3 {
		t.Errorf("news %v keep %v", news, keep)
	}
}
