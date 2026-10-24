package app

import (
	"bytes"
	"cmp"
	"maps"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"berthier/internal/core"
)

// ProfileOpts selects what `berthier profile` shows.
type ProfileOpts struct {
	WeeksAgo int
	Corps    string
	Session  string
	WasteMax int
}

// Profile folds new transcript lines and computes the Profile view.
func (e *Env) Profile(o ProfileOpts) (core.ProfileView, error) {
	if err := e.UpdateProfile(); err != nil {
		return core.ProfileView{}, err
	}
	u, err := e.UpdateUsage()
	if err != nil {
		return core.ProfileView{}, err
	}
	return e.profileView(u, o), nil
}

// profileView computes the view from the index as it is, without folding.
func (e *Env) profileView(u core.Usage, o ProfileOpts) core.ProfileView {
	now := float64(e.now())
	since := core.WeekStart(now, u.ResetsAt) - float64(e.ProfileWeeks-1)*core.Week
	v := u.View(nil, int64(now))
	turns := e.LoadTurns()
	in := core.ProfileIn{Turns: turns, Archive: e.ReadArchive(), Landed: e.Landed(since, mainBranches(turns)), Now: now,
		ResetsAt: u.ResetsAt, WeekPct: v.WeekPct, ByCorps: map[string]float64{},
		WeeksAgo: o.WeeksAgo, Corps: core.CorpsName(o.Corps), Session: o.Session, WasteMax: o.WasteMax}
	for _, c := range v.Corps {
		in.ByCorps[c.Name] = c.Used
	}
	return core.Profile(in)
}

// cmdProfile prints the Profile view as JSON: Spend of a quota week by Corps,
// Division and session, with Spend per Delivered unit and ranked Waste
// signals. --waste prints only the ranked Waste signals (all of them).
func cmdProfile(e *Env, args []string, io IO) error {
	const usage = "usage: berthier profile [--waste] [--corps <corps>] [--session <id>] [--week <weeks ago>]"
	var o ProfileOpts
	waste := false
	for i := 0; i < len(args); i++ {
		val := func() (string, error) {
			if i+1 >= len(args) || args[i+1] == "" {
				return "", die("%s", usage)
			}
			i++
			return args[i], nil
		}
		var err error
		switch args[i] {
		case "--waste":
			waste = true
		case "--corps":
			o.Corps, err = val()
		case "--session":
			o.Session, err = val()
		case "--week":
			var s string
			if s, err = val(); err == nil {
				if o.WeeksAgo, err = strconv.Atoi(s); err != nil || o.WeeksAgo < 0 {
					return die("%s", usage)
				}
			}
		default:
			return die("%s", usage)
		}
		if err != nil {
			return err
		}
	}
	if waste {
		o.WasteMax = 1 << 30
	}
	v, err := e.Profile(o)
	if err != nil {
		return err
	}
	if waste {
		return encode(io.Out, struct {
			Week  core.WeekSpan    `json:"week"`
			Waste []core.WasteItem `json:"waste"`
		}{v.Week, v.Waste})
	}
	return encode(io.Out, v)
}

// --- Delivered units from git

type landedKey struct {
	root, branch, head string
	since              float64
}

var landedCache sync.Map // landedKey -> []core.Landed

// mainBranches lists, per Corps, the branches its Main Agent worked on, most
// worked first: where its integration branch is to be found.
func mainBranches(turns []core.Turn) map[string][]string {
	counts := map[string]map[string]int{}
	for _, t := range turns {
		if t.Division != "" || t.Branch == "" || t.Branch == "HEAD" {
			continue
		}
		if counts[t.Corps] == nil {
			counts[t.Corps] = map[string]int{}
		}
		counts[t.Corps][t.Branch]++
	}
	out := map[string][]string{}
	for c, m := range counts {
		l := slices.Sorted(maps.Keys(m))
		slices.SortStableFunc(l, func(a, b string) int { return cmp.Compare(m[b], m[a]) })
		out[c] = l
	}
	return out
}

// Landed lists every commit since a time on the main branch of every Corps's
// repository (first parents; a merge counts the commits it brought), for
// core.Credit to give to the Division that delivered it. Berthier's own
// commits are Command overhead.
func (e *Env) Landed(since float64, branches map[string][]string) []core.Landed {
	st := e.ReadState()
	b := "berthier"
	if a := st.Agents.Get(strOr(st.Berthier)); a != nil && a.WorkspaceLabel != nil {
		b = core.CorpsName(*a.WorkspaceLabel)
	}
	out := []core.Landed{}
	for _, c := range st.Corps {
		if c.RepoRoot != nil {
			n := core.CorpsName(c.Name)
			out = append(out, landedOn(*c.RepoRoot, branches[n], n, n, since)...)
		}
	}
	if e.Root != "" {
		out = append(out, landedOn(e.Root, branches[core.Overhead], b, core.Overhead, since)...)
	}
	return out
}

// landedOn reads the integration branch among the Main Agent's branches (at
// most four, most worked first): the one where the most pull requests and
// merges landed, ties to the most worked (a feature branch holds its
// base's landings too). skipli's Main Agent sits on master
// and feature branches, but its pull requests land on dev.
func landedOn(root string, branches []string, corps, main string, since float64) []core.Landed {
	if len(branches) > 4 {
		branches = branches[:4]
	}
	var best []core.Landed
	bestN := -1
	for _, b := range append(branches, "") {
		l := landedIn(root, b, corps, main, since)
		n := 0
		for _, x := range l {
			if x.Merge || squashed.MatchString(x.Subject) {
				n++
			}
		}
		if n > bestN {
			best, bestN = l, n
		}
	}
	return best
}

var squashed = regexp.MustCompile(`\(#\d+\)\s*$`)

// landedIn reads branch's first-parent history in root (HEAD when branch is
// empty or unknown).
func landedIn(root, branch, corps, main string, since float64) []core.Landed {
	ref := "HEAD"
	if branch != "" && exec.Command("git", "-C", root, "rev-parse", "--verify", "-q", branch).Run() == nil {
		ref = branch
	}
	head, err := exec.Command("git", "-C", root, "rev-parse", ref).Output()
	if err != nil {
		return nil
	}
	k := landedKey{root, ref, strings.TrimSpace(string(head)), since}
	if v, ok := landedCache.Load(k); ok {
		return v.([]core.Landed)
	}
	out, err := exec.Command("git", "-C", root, "log", "--first-parent", "--since="+time.Unix(int64(since), 0).UTC().Format(time.RFC3339),
		"--format=%H%x1f%ct%x1f%P%x1f%s", ref).Output()
	if err != nil {
		return nil
	}
	var l []core.Landed
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) != 4 {
			continue
		}
		at, _ := strconv.ParseFloat(f[1], 64)
		parents := strings.Fields(f[2])
		x := core.Landed{Corps: corps, Main: main, Commits: 1, At: at, Subject: f[3], Sha: f[0]}
		if len(parents) == 2 {
			x.Merge = true
			if n, err := exec.Command("git", "-C", root, "rev-list", "--count", parents[0]+".."+parents[1]).Output(); err == nil {
				c, _ := strconv.Atoi(strings.TrimSpace(string(n)))
				x.Commits = max(c, 1)
			}
		}
		l = append(l, x)
	}
	landedCache.Store(k, l)
	return l
}

type profileKey struct {
	week    int
	session string
}

type profileHit struct {
	at   time.Time
	body []byte
}

var profileCache sync.Map // profileKey -> profileHit

// profileJSON is the Console's Profile: the view of the index as the Main
// Loop last folded it (the Console never folds), kept for 15 seconds.
func (e *Env) profileJSON(weeksAgo int, session string) ([]byte, error) {
	k := profileKey{weeksAgo, session}
	if v, ok := profileCache.Load(k); ok && time.Since(v.(profileHit).at) < 15*time.Second {
		return v.(profileHit).body, nil
	}
	var u core.Usage
	readJSON(e.Usage, &u)
	var b bytes.Buffer
	if err := encode(&b, e.profileView(u, ProfileOpts{WeeksAgo: weeksAgo, Session: session})); err != nil {
		return nil, err
	}
	profileCache.Store(k, profileHit{time.Now(), b.Bytes()})
	return b.Bytes(), nil
}
