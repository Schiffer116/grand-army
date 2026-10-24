package core

import (
	"maps"
	"math"
	"slices"
	"sort"
)

// Weekly quota attribution, pure. `berthier sample` appends one Row per change
// of a session's status line to the ledger: the account's week_pct, resets_at
// and five-hour window. Fold turns new rows into the usage record
// (state/usage.json), so each Corps knows how much of the account's seven-day
// quota it used (ADR 0005).
//
// Each rise in week_pct is split across the Spend of the turns since the
// previous rise (from the transcripts, SpendAt), by Corps. A rise no Spend
// explains (claude.ai, another machine) goes to "other". The first row ever
// seen is only a baseline: what the account used before sampling started
// belongs to no one.

// Row is one ledger line.
type Row struct {
	Ts           float64  `json:"ts"`
	Session      string   `json:"session"`
	Cwd          *string  `json:"cwd"`
	Cost         float64  `json:"cost"`
	WeekPct      float64  `json:"week_pct"`
	ResetsAt     *float64 `json:"resets_at"`
	FivePct      *float64 `json:"five_pct,omitempty"`
	FiveResetsAt *float64 `json:"five_resets_at,omitempty"`
}

// SessionUse is what the record remembers of a session: its status line cost.
type SessionUse struct {
	Cost float64 `json:"cost"`
	Ts   float64 `json:"ts"`
}

// SpendAt is Spend credited to a Corps at a time: one Profile row.
type SpendAt struct {
	Ts    float64
	Corps string
	USD   float64
}

// UsageSource marks a record attributed from transcript Spend; one without it
// is refolded from the ledger once.
const UsageSource = "transcripts"

// Usage is state/usage.json.
type Usage struct {
	Source       string                `json:"source"`
	ResetsAt     *float64              `json:"resets_at"`
	WeekPct      *float64              `json:"week_pct"`
	FivePct      *float64              `json:"five_pct,omitempty"`
	FiveResetsAt *float64              `json:"five_resets_at,omitempty"`
	ByCorps      map[string]float64    `json:"by_corps"`
	LastRise     float64               `json:"last_rise"` // Spend after this explains the next rise
	Sessions     map[string]SessionUse `json:"sessions"`
	Offset       int64                 `json:"offset"`
}

// newWindow: a row from another window: resets_at moved or the quota fell
// sharply (a manual reset keeps resets_at), or (no resets_at) the quota fell
// at all.
func (u *Usage) newWindow(r Row) bool {
	if u.WeekPct == nil {
		return false
	}
	if r.ResetsAt != nil && u.ResetsAt != nil {
		return math.Abs(*r.ResetsAt-*u.ResetsAt) > 3600 || *u.WeekPct-r.WeekPct >= 10 || r.WeekPct < *u.WeekPct/2
	}
	return r.WeekPct < *u.WeekPct
}

// stale: a row without five_pct once the record has seen it is a session's
// first render, carrying a cached week_pct. Its cost counts, its week_pct does not.
func (u *Usage) stale(r Row) bool { return u.FivePct != nil && r.FivePct == nil }

// rise splits amt across the Spend in (LastRise, until], by Corps. Rows carry
// whole seconds and Spend milliseconds: until is the row's second, plus one.
func (u *Usage) rise(amt float64, spend []SpendAt, until float64) {
	i := sort.Search(len(spend), func(i int) bool { return spend[i].Ts > u.LastRise })
	acc := map[string]float64{}
	total := 0.0
	for ; i < len(spend) && spend[i].Ts <= until; i++ {
		acc[spend[i].Corps] += spend[i].USD
		total += spend[i].USD
	}
	if total > 0 {
		for _, k := range slices.Sorted(maps.Keys(acc)) {
			u.ByCorps[k] += amt * acc[k] / total
		}
	} else {
		u.ByCorps["other"] += amt
	}
}

func (u *Usage) step(r Row, spend []SpendAt) {
	stale := u.stale(r)
	if !stale && u.newWindow(r) {
		z := 0.0
		u.WeekPct = &z
		u.ByCorps = map[string]float64{}
	}
	u.Sessions[r.Session] = SessionUse{Cost: r.Cost, Ts: r.Ts}
	if !stale {
		switch {
		case u.WeekPct == nil:
			w := r.WeekPct
			u.WeekPct = &w
			u.LastRise = r.Ts + 1
		case r.WeekPct > *u.WeekPct:
			u.rise(r.WeekPct-*u.WeekPct, spend, r.Ts+1)
			u.LastRise = r.Ts + 1
			w := r.WeekPct
			u.WeekPct = &w
		}
		if r.ResetsAt != nil {
			u.ResetsAt = r.ResetsAt
		}
	}
	if r.FivePct != nil {
		u.FivePct, u.FiveResetsAt = r.FivePct, r.FiveResetsAt
	}
}

// Fold returns u folded over rows in ledger order, splitting each rise by
// spend (sorted by time); sessions idle for two weeks are forgotten. offset
// is how many ledger bytes the record has seen.
func (u Usage) Fold(rows []Row, spend []SpendAt, offset int64) Usage {
	u.Source = UsageSource
	u.ByCorps = maps.Collect(maps.All(u.ByCorps))
	sessions := make(map[string]SessionUse, len(u.Sessions))
	for k, v := range u.Sessions {
		sessions[k] = v
	}
	u.Sessions = sessions
	latest := 0.0
	for _, r := range rows {
		u.step(r, spend)
		latest = max(latest, r.Ts)
	}
	for k, s := range u.Sessions {
		if !(s.Ts > latest-1209600) {
			delete(u.Sessions, k)
		}
	}
	u.Offset = offset
	return u
}

// CorpsUse is one Corps's share of the week against its allowance.
type CorpsUse struct {
	Name      string   `json:"name"`
	Used      float64  `json:"used"`
	Allowance *float64 `json:"allowance"`
	Ratio     *float64 `json:"ratio"`
}

// FiveHour is the account's five-hour window.
type FiveHour struct {
	Pct      float64  `json:"pct"`
	ResetsAt *float64 `json:"resets_at"`
}

// UsageView is the usage record read for people and the Main Loop.
type UsageView struct {
	ResetsAt *float64   `json:"resets_at"`
	WeekPct  *float64   `json:"week_pct"`
	FiveHour *FiveHour  `json:"five_hour"`
	Corps    []CorpsUse `json:"corps"`
}

// View lists each Corps with usage or a budget this week, hungriest first
// (allowance and ratio nil without a budget). A record whose week has ended
// counts for nothing until the next sample.
func (u Usage) View(budgets map[string]float64, now int64) UsageView {
	n := float64(now)
	live := orF(u.ResetsAt, n+1) > n
	used := map[string]float64{}
	if live {
		used = u.ByCorps
	}
	b := map[string]float64{}
	for k, v := range budgets {
		b[CorpsName(k)] = v
	}
	names := map[string]bool{}
	for k := range used {
		names[k] = true
	}
	for k := range b {
		names[k] = true
	}
	v := UsageView{ResetsAt: u.ResetsAt, Corps: []CorpsUse{}}
	if live {
		v.WeekPct = u.WeekPct
	}
	if u.FivePct != nil && orF(u.FiveResetsAt, n+1) > n {
		v.FiveHour = &FiveHour{*u.FivePct, u.FiveResetsAt}
	}
	for name := range names {
		c := CorpsUse{Name: name, Used: used[name]}
		if a, ok := b[name]; ok {
			c.Allowance = &a
			if a > 0 {
				r := c.Used / a
				c.Ratio = &r
			}
		}
		v.Corps = append(v.Corps, c)
	}
	sort.SliceStable(v.Corps, func(i, j int) bool {
		a, b := v.Corps[i], v.Corps[j]
		if a.Used != b.Used {
			return a.Used > b.Used
		}
		return a.Name < b.Name
	})
	return v
}

func orF(p *float64, d float64) float64 {
	if p == nil {
		return d
	}
	return *p
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
