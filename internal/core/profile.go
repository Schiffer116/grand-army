package core

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// The Profile view, pure: Spend of one quota week broken down Corps ->
// Division -> session -> turn, by model, token kind, cache hit rate and Turn
// origin, with Spend per Delivered unit and ranked Waste signals. One view
// model for `berthier profile`, the Console and optimizing Agents.

// Week is a quota week in seconds.
const Week = 7 * 86400

// WeekStart is when the quota week holding now began: resets_at minus whole
// weeks, or (no quota seen yet) weeks counted from the Unix epoch.
func WeekStart(now float64, resetsAt *float64) float64 {
	anchor := 0.0
	if resetsAt != nil {
		anchor = *resetsAt
	}
	return anchor + math.Floor((now-anchor)/Week)*Week
}

// Archive is per-session totals of the weeks the index no longer keeps turn
// by turn.
type Archive map[string]*ArchivedSession

// ArchivedSession is one compacted session.
type ArchivedSession struct {
	Corps    string            `json:"corps"`
	Division string            `json:"division,omitempty"`
	Start    float64           `json:"start"`
	End      float64           `json:"end"`
	Turns    int               `json:"turns"`
	Models   map[string]Tokens `json:"models"`
	USD      float64           `json:"usd"`
}

// Add folds a turn into its session's totals.
func (a Archive) Add(t Turn) {
	s := a[t.Session]
	if s == nil {
		s = &ArchivedSession{Corps: t.Corps, Division: t.Division, Start: t.Start, Models: map[string]Tokens{}}
		a[t.Session] = s
	}
	s.Start = min(s.Start, t.Start)
	s.End = max(s.End, t.End)
	s.Turns++
	m := s.Models[t.Model]
	m.Add(t.Tokens)
	s.Models[t.Model] = m
	s.USD += t.USD
}

// Totals is Spend summed over turns.
type Totals struct {
	Tokens
	USD      float64 `json:"usd"`
	Calls    int     `json:"calls"`
	Turns    int     `json:"turns"`
	CacheHit float64 `json:"cache_hit"` // share of prompt tokens read from cache
	Unpriced bool    `json:"unpriced,omitempty"`
}

func (s *Totals) add(t Turn) {
	s.Tokens.Add(t.Tokens)
	s.USD += t.USD
	s.Calls += t.Calls
	s.Turns++
	s.Unpriced = s.Unpriced || t.Unpriced
	if p := s.Prompt(); p > 0 {
		s.CacheHit = round3(float64(s.CR) / float64(p))
	}
}

// Landed is Delivered work: a commit on a Corps's main branch (a merge counts
// the commits it brought), credited to its Main Agent (Division "") or to the
// Division that delivered it (Credit), and whether it finished that work.
type Landed struct {
	Corps    string  `json:"corps"`
	Division string  `json:"division,omitempty"`
	Commits  int     `json:"commits"`
	Finished bool    `json:"finished,omitempty"`
	At       float64 `json:"at"`
	Subject  string  `json:"subject,omitempty"`
	Sha      string  `json:"sha,omitempty"`
	Merge    bool    `json:"merge,omitempty"`
	Main     string  `json:"main,omitempty"` // who gets it without a Division: the Corps, or Berthier for its own repo
}

// Units is how many Delivered units it counts for: each commit, plus one for
// finished work (a Division's branch or pull request merged).
func (l Landed) Units() int {
	if l.Finished {
		return l.Commits + 1
	}
	return l.Commits
}

var (
	squashPR    = regexp.MustCompile(`\(#(\d+)\)\s*$`)
	mergePR     = regexp.MustCompile(`^Merge pull request #(\d+) from [^/\s]+/(\S+)`)
	mergeBranch = regexp.MustCompile(`^Merge branch '([^']+)'`)
)

// Credit finds which Division delivered each landed commit, from what the
// Divisions' turns shipped and worked on: a squash-merged or merged pull
// request ("(#28)", "Merge pull request #28") a Division created, a commit
// whatever way it landed (fast-forward, rebase keeping its sha) a Division
// made, a merged branch a Division worked. Merged branches and pull requests
// finish that work. The rest is the Main Agent's (Landed.Main).
func Credit(landed []Landed, turns []Turn) []Landed {
	pr, sha, branch := map[string]string{}, map[string]string{}, map[string]string{}
	for _, t := range turns {
		if t.Division == "" {
			continue
		}
		for _, x := range t.Ships {
			kind, v, _ := strings.Cut(x, " ")
			switch kind {
			case "pr":
				pr[t.Corps+"#"+v] = t.Division
			case "commit":
				sha[t.Corps+"@"+v] = t.Division
			}
		}
		if t.Branch != "" && t.Branch != "HEAD" {
			branch[t.Corps+":"+t.Branch] = t.Division
		}
	}
	out := make([]Landed, 0, len(landed))
	for _, l := range landed {
		if l.Division == "" {
			div, finished := "", false
			if m := mergePR.FindStringSubmatch(l.Subject); m != nil {
				div, finished = pr[l.Corps+"#"+m[1]], true
				if div == "" {
					div = branch[l.Corps+":"+m[2]]
				}
			} else if m := squashPR.FindStringSubmatch(l.Subject); m != nil && pr[l.Corps+"#"+m[1]] != "" {
				div, finished = pr[l.Corps+"#"+m[1]], true
			} else if m := mergeBranch.FindStringSubmatch(l.Subject); m != nil && l.Merge {
				div, finished = branch[l.Corps+":"+m[1]], true
				if div == "" {
					div = CorpsName(m[1])
				}
			} else if len(l.Sha) >= 7 {
				div = sha[l.Corps+"@"+l.Sha[:7]]
			}
			if div != "" {
				l.Division, l.Finished = div, l.Finished || finished
			} else if l.Main != "" {
				l.Corps = l.Main
			}
		}
		out = append(out, l)
	}
	return out
}

// Line is one row of the Profile table: a Corps, a Division, or Command overhead.
type Line struct {
	Name     string             `json:"name"`
	Totals   Totals             `json:"totals"`
	Units    int                `json:"units"`
	PerUnit  *float64           `json:"usd_per_unit"`
	QuotaPct *float64           `json:"quota_pct,omitempty"`
	Daily    []float64          `json:"daily"`
	Models   map[string]Totals  `json:"models"`
	Origins  map[string]float64 `json:"origins"`
	Waste    float64            `json:"waste_usd"`
	// A Corps lists its Divisions (Name "" is its Main Agent); a Division its sessions.
	Divisions []Line        `json:"divisions,omitempty"`
	Sessions  []SessionLine `json:"sessions,omitempty"`
}

// SessionLine is one session of a Division or Main Agent.
type SessionLine struct {
	ID     string  `json:"id"`
	Parent string  `json:"parent,omitempty"`
	Model  string  `json:"model"` // the model that Spent most
	Origin string  `json:"origin"`
	Prompt string  `json:"prompt,omitempty"`
	Branch string  `json:"branch,omitempty"`
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
	Totals Totals  `json:"totals"`
}

// WasteItem is one ranked Waste signal: a kind, in one session, with the turn
// that shows it worst and what it cost.
type WasteItem struct {
	Kind     string  `json:"kind"` // bloat, cache_miss, big_result, reread, model
	Corps    string  `json:"corps"`
	Division string  `json:"division,omitempty"`
	Session  string  `json:"session"`
	Turn     string  `json:"turn"` // the worst turn's id
	Turns    int     `json:"turns"`
	Model    string  `json:"model"`
	Tokens   int64   `json:"tokens"`
	USD      float64 `json:"usd"`
	At       float64 `json:"at"`
	Evidence string  `json:"evidence"`
}

// ProfileIn is what the view is computed from.
type ProfileIn struct {
	Turns    []Turn
	Archive  Archive
	Landed   []Landed
	Now      float64
	ResetsAt *float64
	WeekPct  *float64           // the account's, live week only
	ByCorps  map[string]float64 // quota % by Corps (usage.json), live week only
	WeeksAgo int                // 0: the current quota week
	Corps    string             // only this Corps (or Division), if set
	Session  string             // list this session's turns
	WasteMax int                // waste items to list (default 20)
}

// ProfileView is `berthier profile`.
type ProfileView struct {
	Week     WeekSpan          `json:"week"`
	WeekPct  *float64          `json:"week_pct"`
	Total    Line              `json:"total"`
	Corps    []Line            `json:"corps"`
	Overhead *Line             `json:"overhead"`
	Models   map[string]Totals `json:"models"`
	Waste    []WasteItem       `json:"waste"`
	Weeks    []WeekSpend       `json:"weeks"`
	Turns    []Turn            `json:"turns,omitempty"`
}

// WeekSpan is the quota week shown.
type WeekSpan struct {
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
	WeeksAgo int     `json:"weeks_ago"`
	Live     bool    `json:"live"`
}

// WeekSpend is a past week's Spend by Corps, for trends.
type WeekSpend struct {
	Start   float64            `json:"start"`
	USD     float64            `json:"usd"`
	ByCorps map[string]float64 `json:"by_corps"`
}

func newLine(name string) Line {
	return Line{Name: name, Daily: make([]float64, 7), Models: map[string]Totals{}, Origins: map[string]float64{}}
}

func (l *Line) add(t Turn, start float64) {
	l.Totals.add(t)
	if d := int((t.Start - start) / 86400); d >= 0 && d < 7 {
		l.Daily[d] = round2(l.Daily[d] + t.USD)
	} else if d >= 7 {
		l.Daily[6] = round2(l.Daily[6] + t.USD)
	}
	m := l.Models[t.Model]
	m.add(t)
	l.Models[t.Model] = m
	l.Origins[t.Origin] = round4(l.Origins[t.Origin] + t.USD)
}

func (l *Line) finish(units int) {
	l.Units = units
	l.Totals.USD = round4(l.Totals.USD)
	if units > 0 {
		v := round4(l.Totals.USD / float64(units))
		l.PerUnit = &v
	}
	for k, m := range l.Models {
		m.USD = round4(m.USD)
		l.Models[k] = m
	}
}

// Profile computes the view.
func Profile(in ProfileIn) ProfileView {
	cur := WeekStart(in.Now, in.ResetsAt)
	start := cur - float64(in.WeeksAgo)*Week
	end := start + Week
	v := ProfileView{Week: WeekSpan{start, end, in.WeeksAgo, in.WeeksAgo == 0}, Models: map[string]Totals{}, Corps: []Line{}, Waste: []WasteItem{}}
	if in.WeeksAgo == 0 {
		v.WeekPct = in.WeekPct
	}
	match := func(t Turn) bool {
		return in.Corps == "" || t.Corps == in.Corps || t.Division == in.Corps
	}
	var week []Turn
	for _, t := range in.Turns {
		if t.Start >= start && t.Start < end && match(t) {
			week = append(week, t)
		}
	}
	units := map[[2]string]int{}
	for _, l := range Credit(in.Landed, in.Turns) {
		if l.At >= start && l.At < end {
			units[[2]string{l.Corps, l.Division}] += l.Units()
		}
	}

	total := newLine("total")
	corps := map[string]*Line{}
	divs := map[[2]string]*Line{}
	sessions := map[string]*SessionLine{}
	sessionPlace := map[string][2]string{}
	sessionModels := map[string]map[string]float64{}
	for _, t := range week {
		total.add(t, start)
		m := v.Models[t.Model]
		m.add(t)
		v.Models[t.Model] = m
		c := corps[t.Corps]
		if c == nil {
			l := newLine(t.Corps)
			c = &l
			corps[t.Corps] = c
		}
		c.add(t, start)
		k := [2]string{t.Corps, t.Division}
		d := divs[k]
		if d == nil {
			l := newLine(t.Division)
			d = &l
			divs[k] = d
		}
		d.add(t, start)
		s := sessions[t.Session]
		if s == nil {
			s = &SessionLine{ID: t.Session, Parent: t.Parent, Origin: t.Origin, Prompt: t.Prompt, Branch: t.Branch, Start: t.Start}
			sessions[t.Session] = s
			sessionPlace[t.Session] = k
			sessionModels[t.Session] = map[string]float64{}
		}
		s.Totals.add(t)
		s.End = max(s.End, t.End)
		sessionModels[t.Session][t.Model] += t.USD
	}
	waste := Waste(week)
	wasteBy := map[[2]string]float64{}
	for _, w := range waste {
		wasteBy[[2]string{w.Corps, w.Division}] += w.USD
		wasteBy[[2]string{w.Corps, "*"}] += w.USD
	}
	for id, s := range sessions {
		s.Model = topKey(sessionModels[id])
		s.Totals.USD = round4(s.Totals.USD)
		d := divs[sessionPlace[id]]
		d.Sessions = append(d.Sessions, *s)
	}
	for k, d := range divs {
		sort.Slice(d.Sessions, func(i, j int) bool { return d.Sessions[i].Totals.USD > d.Sessions[j].Totals.USD })
		d.finish(units[k])
		d.Waste = round4(wasteBy[k])
		corps[k[0]].Divisions = append(corps[k[0]].Divisions, *d)
	}
	sumUnits := map[string]int{}
	for k, n := range units {
		sumUnits[k[0]] += n
	}
	for name, c := range corps {
		sort.Slice(c.Divisions, func(i, j int) bool { return c.Divisions[i].Totals.USD > c.Divisions[j].Totals.USD })
		c.finish(sumUnits[name])
		c.Waste = round4(wasteBy[[2]string{name, "*"}])
		if in.WeeksAgo == 0 {
			if p, ok := in.ByCorps[name]; ok {
				p = round2(p)
				c.QuotaPct = &p
			}
		}
		if name == Overhead {
			v.Overhead = c
		} else {
			v.Corps = append(v.Corps, *c)
		}
	}
	sort.Slice(v.Corps, func(i, j int) bool {
		if v.Corps[i].Totals.USD != v.Corps[j].Totals.USD {
			return v.Corps[i].Totals.USD > v.Corps[j].Totals.USD
		}
		return v.Corps[i].Name < v.Corps[j].Name
	})
	n := 0
	for _, u := range units {
		n += u
	}
	total.finish(n)
	for _, w := range waste {
		total.Waste += w.USD
	}
	total.Waste = round4(total.Waste)
	v.Total = total
	for k, m := range v.Models {
		m.USD = round4(m.USD)
		v.Models[k] = m
	}
	max := in.WasteMax
	if max == 0 {
		max = 20
	}
	if len(waste) > max {
		waste = waste[:max]
	}
	v.Waste = append(v.Waste, waste...)
	if in.Session != "" {
		for _, t := range week {
			if t.Session == in.Session || t.Parent == in.Session {
				v.Turns = append(v.Turns, t)
			}
		}
		if v.Turns == nil {
			v.Turns = []Turn{}
		}
	}
	v.Weeks = weekly(in, cur, match)
	return v
}

// weekly is each kept and archived week's Spend by Corps, newest first.
func weekly(in ProfileIn, cur float64, match func(Turn) bool) []WeekSpend {
	byStart := map[float64]*WeekSpend{}
	add := func(at float64, corps string, usd float64) {
		s := cur - math.Ceil((cur-at)/Week)*Week
		if at >= cur {
			s = cur
		}
		w := byStart[s]
		if w == nil {
			w = &WeekSpend{Start: s, ByCorps: map[string]float64{}}
			byStart[s] = w
		}
		w.USD = round4(w.USD + usd)
		w.ByCorps[corps] = round4(w.ByCorps[corps] + usd)
	}
	for _, t := range in.Turns {
		if match(t) {
			add(t.Start, t.Corps, t.USD)
		}
	}
	for _, s := range in.Archive {
		if in.Corps == "" || s.Corps == in.Corps || s.Division == in.Corps {
			add(s.Start, s.Corps, s.USD)
		}
	}
	out := []WeekSpend{}
	for _, w := range byStart {
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start > out[j].Start })
	return out
}

// Strong models whose short turns a cheaper one could do, and that one.
const cheaperModel = "claude-sonnet-5-5"

func strongModel(m string) bool {
	return strings.Contains(m, "opus") || strings.Contains(m, "fable") || strings.Contains(m, "mythos")
}

// writePrice is what a token written to cache cost in this turn, per token.
func writePrice(t Turn) float64 {
	p, ok := PriceOf(t.Model)
	if !ok {
		return 0
	}
	w := t.Writes()
	mult := 1.25
	if w > 0 {
		mult = (float64(t.W5m+t.Unknown)*1.25 + float64(t.W1h)*2) / float64(w)
	}
	return p.In * mult / 1e6
}

// Waste ranks the Waste signals in turns, by what they cost, one item per kind
// and session.
func Waste(turns []Turn) []WasteItem {
	type key struct{ kind, session string }
	items := map[key]*WasteItem{}
	worst := map[key]float64{}
	var order []key
	note := func(kind string, t Turn, tokens int64, usd float64) {
		if usd <= 0 {
			return
		}
		k := key{kind, t.Session}
		w := items[k]
		if w == nil {
			w = &WasteItem{Kind: kind, Corps: t.Corps, Division: t.Division, Session: t.Session, Model: t.Model}
			items[k] = w
			order = append(order, k)
		}
		w.Turns++
		w.Tokens += tokens
		w.USD += usd
		if usd > worst[k] {
			worst[k], w.Turn, w.At = usd, t.ID, t.Start
		}
	}
	ctxMax := map[key]int64{}
	bigTools := map[key][]string{}
	rereads := map[key][]string{}
	for _, t := range turns {
		p, priced := PriceOf(t.Model)
		if !priced {
			continue
		}
		wp := writePrice(t)
		note("bloat", t, t.Bloat, float64(t.Bloat)*p.CacheRead/1e6)
		if k := (key{"bloat", t.Session}); t.Bloat > 0 {
			ctxMax[k] = max(ctxMax[k], t.CtxMax)
		}
		note("cache_miss", t, t.Miss, float64(t.Miss)*(wp-p.CacheRead/1e6))
		note("big_result", t, t.Big, float64(t.Big)*wp)
		if k := (key{"big_result", t.Session}); t.Big > 0 && t.BigTool != "" && !slices.Contains(bigTools[k], t.BigTool) {
			bigTools[k] = append(bigTools[k], t.BigTool)
		}
		note("reread", t, t.Reread, float64(t.Reread)*wp)
		if k := (key{"reread", t.Session}); t.Reread > 0 {
			for _, r := range t.Rereads {
				if len(rereads[k]) < 5 && !slices.Contains(rereads[k], r) {
					rereads[k] = append(rereads[k], r)
				}
			}
		}
		if strongModel(t.Model) && t.Calls <= 2 && t.Out < 2000 && t.Tools <= 1 && t.Origin != "subagent" {
			cheap := Prices[cheaperModel].USD(t.Tokens, false)
			note("model", t, t.Out, t.USD-cheap)
		}
	}
	out := make([]WasteItem, 0, len(order))
	for _, k := range order {
		w := items[k]
		w.USD = round4(w.USD)
		switch k.kind {
		case "bloat":
			w.Evidence = fmt.Sprintf("context peaked at %s tokens; %s tokens above %s in %d turn(s)", kTok(ctxMax[k]), kTok(w.Tokens), kTok(BloatAt), w.Turns)
		case "cache_miss":
			w.Evidence = fmt.Sprintf("cache rewritten after its TTL expired: %s tokens in %d turn(s)", kTok(w.Tokens), w.Turns)
		case "big_result":
			w.Evidence = fmt.Sprintf("oversized tool results (%s): ~%s tokens in %d turn(s)", strings.Join(bigTools[k], ", "), kTok(w.Tokens), w.Turns)
		case "reread":
			w.Evidence = fmt.Sprintf("read again: %s (~%s tokens)", strings.Join(rereads[k], ", "), kTok(w.Tokens))
		case "model":
			w.Evidence = fmt.Sprintf("%d short turn(s) (<=2 calls, <2k output) on %s; %s would cost $%.2f less", w.Turns, w.Model, cheaperModel, w.USD)
		}
		out = append(out, *w)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].USD > out[j].USD })
	return out
}

// WasteLine is a Corps's top Waste signals in one line, for its budget warning.
func WasteLine(items []WasteItem, corps string, n int) string {
	var parts []string
	for _, w := range items {
		if w.Corps != corps {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s $%.2f (%s)", w.Kind, w.USD, w.Turn))
		if len(parts) == n {
			break
		}
	}
	return strings.Join(parts, ", ")
}

func kTok(n int64) string {
	switch {
	case n >= 1e6:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%dk", (n+500)/1000)
	}
	return fmt.Sprint(n)
}

func topKey(m map[string]float64) string {
	best, bv := "", -1.0
	for k, v := range m {
		if v > bv || (v == bv && k < best) {
			best, bv = k, v
		}
	}
	return best
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
func round3(f float64) float64 { return math.Round(f*1000) / 1000 }
func round4(f float64) float64 { return math.Round(f*10000) / 10000 }
