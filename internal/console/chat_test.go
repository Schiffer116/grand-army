package console

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"berthier/internal/claude/claudetest"
	"berthier/internal/harness"
	"berthier/internal/host/hosttest"
)

func getJSON(t *testing.T, url string) map[string]any {
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("GET %s = %d", url, res.StatusCode)
	}
	var v map[string]any
	json.NewDecoder(res.Body).Decode(&v)
	return v
}

func TestChatShowsTranscriptOrTerminal(t *testing.T) {
	r := newRig(t, "takes") // four Claude Agents, session s1, status idle
	r.ports.Harness = claudetest.Fake{Chats: map[string]harness.Chat{
		"s1": {Session: "s1", Rev: 7, Items: []harness.ChatItem{{Role: "user", Text: "hi"}}, Info: harness.AgentInfo{Model: "Opus 5.5"}},
	}}
	a := NewArmy()
	a.Set([]byte(promptArmy))

	srv := httptest.NewServer(Handler(a, r.ports, nil))
	defer srv.Close()
	v := getJSON(t, srv.URL+"/api/chat?pane=w1:p1")
	if v["source"] != "transcript" || v["session"] != "s1" || v["status"] != "idle" || len(v["items"].([]any)) != 1 ||
		v["info"].(map[string]any)["model"] != "Opus 5.5" {
		t.Fatalf("claude agent: %v", v)
	}
	if res, _ := http.Get(srv.URL + "/api/chat?pane=wZ:p9"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown pane = %d", res.StatusCode)
	}

	// A session with no record, and an Agent that is not Claude: the terminal.
	r.ports.Harness = claudetest.Fake{}
	srv2 := httptest.NewServer(Handler(a, r.ports, nil))
	defer srv2.Close()
	if v := getJSON(t, srv2.URL+"/api/chat?pane=w1:p1"); v["source"] != "terminal" || !strings.Contains(v["text"].(string), "box: ") {
		t.Fatalf("no record: %v", v)
	}
	other := "codex"
	r.host.Update("w2:p1", func(p *hosttest.Pane) { p.Agent = &other })
	if v := getJSON(t, srv.URL+"/api/chat?pane=w2:p1"); v["source"] != "terminal" || v["kind"] != "codex" {
		t.Fatalf("other agent: %v", v)
	}
}

func TestSkillsEndpoint(t *testing.T) {
	r := newRig(t, "takes")
	r.ports.Harness = claudetest.Fake{SkillList: []harness.Skill{{Name: "deploy", Desc: "ship it", Src: "project"}}}
	a := NewArmy()
	a.Set([]byte(promptArmy))
	srv := httptest.NewServer(Handler(a, r.ports, nil))
	defer srv.Close()
	v := getJSON(t, srv.URL+"/api/skills?pane=w1:p1")
	if l, _ := v["skills"].([]any); len(l) != 1 || l[0].(map[string]any)["name"] != "deploy" {
		t.Fatalf("skills: %v", v)
	}
	if res, _ := http.Get(srv.URL + "/api/skills?pane=wZ:p9"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown pane = %d", res.StatusCode)
	}
}
