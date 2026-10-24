package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func put(t *testing.T, path, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkills(t *testing.T) {
	home, cwd, plug := t.TempDir(), t.TempDir(), t.TempDir()
	put(t, home+"/skills/deploy/SKILL.md", "---\nname: deploy\ndescription: >\n  Ship it\n  to prod\n---\nbody")
	put(t, home+"/skills/shadow/SKILL.md", "---\nname: shadow\ndescription: user one\n---\n")
	put(t, cwd+"/.claude/skills/shadow/SKILL.md", "---\nname: shadow\ndescription: project one\n---\n")
	put(t, cwd+"/.claude/skills/hidden/SKILL.md", "---\nname: hidden\nuser-invocable: false\ndescription: x\n---\n")
	put(t, cwd+"/.claude/commands/fix/lint.md", "# Fix lint errors\nmore")
	put(t, home+"/commands/clear.md", "---\ndescription: mine\n---\n")
	put(t, plug+"/.claude-plugin/plugin.json", `{"skills":["./skills/a/tdd"]}`)
	put(t, plug+"/skills/a/tdd/SKILL.md", "---\nname: tdd\ndescription: \"Test first\"\n---\n")
	put(t, plug+"/skills/a/unlisted/SKILL.md", "---\nname: unlisted\ndescription: no\n---\n")
	put(t, plug+"/commands/hi.md", "Say hi")
	put(t, home+"/plugins/installed_plugins.json", `{"plugins":{
	 "pp@mk":[{"scope":"user","installPath":"`+plug+`"}],
	 "other@mk":[{"scope":"project","projectPath":"/elsewhere","installPath":"`+plug+`"}]}}`)

	got := map[string]Skill{}
	for _, s := range Skills(home, cwd) {
		got[s.Name] = s
	}
	for name, want := range map[string]string{
		"deploy": "Ship it to prod", "shadow": "project one", "fix:lint": "Fix lint errors",
		"clear": "mine", "pp:tdd": "Test first", "pp:hi": "Say hi", "compact": "Summarize the conversation to free up context",
	} {
		if got[name].Desc != want {
			t.Errorf("%s = %q, want %q", name, got[name].Desc, want)
		}
	}
	for _, name := range []string{"hidden", "pp:unlisted", "other:tdd", "tdd"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s listed", name)
		}
	}
	if got["shadow"].Src != "project" || got["pp:tdd"].Src != "plugin" {
		t.Errorf("sources: %v", got)
	}
}
