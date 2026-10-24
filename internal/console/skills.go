package console

// The Console's slash picker lists what an Agent's Claude Code can run as
// /<name>: skills, custom commands, plugin skills and a few built-ins.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

type Skill struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
	Src  string `json:"src"` // project, user, plugin or built-in
}

var builtins = []Skill{
	{"clear", "Clear the conversation and free up context", "built-in"},
	{"compact", "Summarize the conversation to free up context", "built-in"},
	{"model", "Switch the model", "built-in"},
	{"context", "Show what is using the context window", "built-in"},
	{"cost", "Show session cost and usage", "built-in"},
	{"resume", "Resume a past conversation", "built-in"},
	{"rewind", "Roll the conversation or code back", "built-in"},
	{"memory", "Edit memory files", "built-in"},
	{"permissions", "Manage tool permissions", "built-in"},
	{"mcp", "Manage MCP servers", "built-in"},
	{"help", "Show help and commands", "built-in"},
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
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
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
	return Skill{prefix + name, clip(strings.Join(strings.Fields(desc), " "), 160), src}, true
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
	ids := make([]string, 0, len(inst.Plugins))
	for id := range inst.Plugins {
		ids = append(ids, id)
	}
	slices.Sort(ids)
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
