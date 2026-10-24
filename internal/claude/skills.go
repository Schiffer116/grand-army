package claude

// The Console's slash picker lists what an Agent's Claude Code can run as
// /<name>: skills, custom commands, plugin skills and a few built-ins.

import (
	"cmp"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"berthier/internal/harness"
)

// Skill is the port's.
type Skill = harness.Skill

var builtins = []Skill{
	{Name: "clear", Desc: "Clear the conversation and free up context", Src: "built-in"},
	{Name: "compact", Desc: "Summarize the conversation to free up context", Src: "built-in"},
	{Name: "model", Desc: "Switch the model", Src: "built-in"},
	{Name: "context", Desc: "Show what is using the context window", Src: "built-in"},
	{Name: "cost", Desc: "Show session cost and usage", Src: "built-in"},
	{Name: "resume", Desc: "Resume a past conversation", Src: "built-in"},
	{Name: "rewind", Desc: "Roll the conversation or code back", Src: "built-in"},
	{Name: "memory", Desc: "Edit memory files", Src: "built-in"},
	{Name: "permissions", Desc: "Manage tool permissions", Src: "built-in"},
	{Name: "mcp", Desc: "Manage MCP servers", Src: "built-in"},
	{Name: "help", Desc: "Show help and commands", Src: "built-in"},
}

// Skills lists what the Claude Code in cwd can invoke, nearest definition
// winning a name: project, then user, then plugins, then built-ins.
func Skills(claudeDir, cwd string) []Skill {
	var all []Skill
	if cwd != "" {
		all = append(all, skillDirs(filepath.Join(cwd, ".claude", "skills"), "", "project")...)
		all = append(all, commandFiles(filepath.Join(cwd, ".claude", "commands"), "", "project")...)
	}
	all = append(all, skillDirs(filepath.Join(claudeDir, "skills"), "", "user")...)
	all = append(all, commandFiles(filepath.Join(claudeDir, "commands"), "", "user")...)
	all = append(all, pluginSkills(claudeDir, cwd)...)
	all = append(all, builtins...)

	seen := map[string]bool{}
	out := []Skill{}
	for _, s := range all {
		if !seen[s.Name] {
			seen[s.Name] = true
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b Skill) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// skillDirs reads <dir>/*/SKILL.md.
func skillDirs(dir, prefix, src string) []Skill {
	var out []Skill
	files, _ := filepath.Glob(filepath.Join(dir, "*", "SKILL.md"))
	for _, f := range files {
		if s, ok := readSkill(f, filepath.Base(filepath.Dir(f)), prefix, src); ok {
			out = append(out, s)
		}
	}
	return out
}

// commandFiles reads <dir>/**/*.md; a subdirectory namespaces: a/b.md is a:b.
func commandFiles(dir, prefix, src string) []Skill {
	var out []Skill
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		name := strings.ReplaceAll(strings.TrimSuffix(rel, ".md"), string(filepath.Separator), ":")
		if s, ok := readSkill(p, name, prefix, src); ok {
			out = append(out, s)
		}
		return nil
	})
	return out
}

// readSkill takes the name and description from a file's frontmatter, falling
// back to fallback and, for a command, its first line of text.
func readSkill(path, fallback, prefix, src string) (Skill, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, false
	}
	fm, body := frontmatter(string(raw))
	if fm["user-invocable"] == "false" {
		return Skill{}, false
	}
	name := fm["name"]
	if name == "" || strings.HasSuffix(path, ".md") && filepath.Base(path) != "SKILL.md" {
		name = fallback
	}
	desc := fm["description"]
	if desc == "" {
		for _, l := range strings.Split(body, "\n") {
			if l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#")); l != "" {
				desc = l
				break
			}
		}
	}
	return Skill{Name: prefix + name, Desc: clipText(strings.Join(strings.Fields(desc), " "), 160), Src: src}, true
}

// frontmatter parses the leading `---` block's top-level `key: value` lines,
// folding indented continuations and `>`/`|` block scalars into the value.
func frontmatter(s string) (map[string]string, string) {
	fm := map[string]string{}
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return fm, s
	}
	key := ""
	for i := 1; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "---" {
			return fm, strings.Join(lines[i+1:], "\n")
		}
		if k, v, ok := strings.Cut(l, ":"); ok && l != "" && l[0] != ' ' && l[0] != '\t' && l[0] != '-' {
			key = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if v == ">" || v == "|" || v == ">-" || v == "|-" {
				v = ""
			}
			fm[key] = strings.Trim(v, `"'`)
		} else if key != "" {
			fm[key] = strings.TrimSpace(fm[key] + " " + strings.TrimSpace(l))
		}
	}
	return fm, ""
}

// pluginSkills reads the plugins installed for the user, or for project cwd,
// as plugin:skill.
func pluginSkills(claudeDir, cwd string) []Skill {
	raw, err := os.ReadFile(filepath.Join(claudeDir, "plugins", "installed_plugins.json"))
	if err != nil {
		return nil
	}
	var inst struct {
		Plugins map[string][]struct {
			Scope, ProjectPath, InstallPath string
		}
	}
	if json.Unmarshal(raw, &inst) != nil {
		return nil
	}
	ids := slices.Sorted(maps.Keys(inst.Plugins))
	var out []Skill
	for _, id := range ids {
		for _, e := range inst.Plugins[id] {
			if e.Scope == "project" && e.ProjectPath != cwd {
				continue
			}
			out = append(out, pluginDir(strings.SplitN(id, "@", 2)[0], e.InstallPath)...)
			break
		}
	}
	return out
}

func pluginDir(plugin, root string) []Skill {
	prefix := plugin + ":"
	var manifest struct{ Skills []string }
	if raw, err := os.ReadFile(filepath.Join(root, ".claude-plugin", "plugin.json")); err == nil {
		json.Unmarshal(raw, &manifest)
	}
	var out []Skill
	if len(manifest.Skills) == 0 {
		out = skillDirs(filepath.Join(root, "skills"), prefix, "plugin")
	}
	for _, d := range manifest.Skills {
		if s, ok := readSkill(filepath.Join(root, d, "SKILL.md"), filepath.Base(d), prefix, "plugin"); ok {
			out = append(out, s)
		}
	}
	return append(out, commandFiles(filepath.Join(root, "commands"), prefix, "plugin")...)
}
