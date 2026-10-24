package claudetest

import (
	"berthier/internal/host"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"berthier/internal/harness"
)

func TestFakeContract(t *testing.T) {
	f := Fake{Dir: t.TempDir(), SkillList: []harness.Skill{{Name: "deploy"}}, Chats: map[string]harness.Chat{
		"chatty-1": {Session: "chatty-1", Items: []harness.ChatItem{{Role: "user", Text: "hi"}, {Role: "assistant", Text: "hello"}}},
	}}
	hook := func(event string) HookCase {
		return HookCase{event, `{"session":"s1","reason":"exit"}`}
	}
	Contract(t, Subject{
		H:          f,
		Draft:      Box("half a thought"),
		Suggestion: Hint("push it"),
		Empty:      Box(""),
		HeldText:   "Reply with just: pong, then stop",
		Held:       Box("Reply with just: pong, then stop"),
		Sent:       Box(""),
		Limit:      Limit(2000),
		Idle:       Box(""),
		LimitSince: 1000,
		LimitReset: 2000,
		Submit:     hook("submit"), Stop: hook("stop"), Permission: hook("permission"), Question: hook("question"),
		Note: hook("note"), End: hook("end"), Start: hook("start"),
		Proc:        host.Process{Name: "claude"},
		OtherProc:   host.Process{Name: "vim"},
		Reason:      "exit",
		OtherReason: "killed",
		HookSession: "s1",
		Write: func(t *testing.T, session string, lines []string) string {
			p := f.Path(session)
			os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return p
		},
		Finished:    []string{"user hi", "done"},
		MidTurn:     []string{"user hi", "cut"},
		Call:        []string{"user hi", fmt.Sprintf("call %d fake-1 5000", 1500), "done"},
		CallAt:      1500,
		CallContext: 5000,
		CallModel:   "fake-1",
		Chat:        []string{"user hi", "assistant hello"},
		ChatItems:   2,
		ChatLast:    "hello",
		Skill:       func(t *testing.T) (string, string) { return t.TempDir(), "deploy" },
		Spend:       [][]byte{[]byte("turn 10 2"), []byte("turn 5 3")},
		SpendIn:     15,
		SpendOut:    5,
		SpendCalls:  2,
		Place:       func(string) (string, string, bool) { return "c", "", true },
	})
}

func TestFakeHooksAreHarnessStates(t *testing.T) {
	if got, _ := (Fake{}).Hook("stop", []byte(`{"session":"x"}`)); got.State != harness.Idle {
		t.Errorf("stop = %+v", got)
	}
}
