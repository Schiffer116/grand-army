package claude

import (
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Classifiers over a pane's screen text: text in, verdict out.

const rule = "────────"

func ruleLines(lines []string) []int {
	var r []int
	for i, l := range lines {
		if strings.HasPrefix(l, rule) {
			r = append(r, i)
		}
	}
	return r
}

var (
	skipLine   = regexp.MustCompile(`^\s*$|^[^\p{L}\p{N}]+ \p{L}+ for [0-9]|^ {20}`)
	limitLine  = regexp.MustCompile(`Usage limit reached|hit your [a-z ]*limit`)
	withTZ     = regexp.MustCompile(`resets ([^·(]+)[(]([^)]+)[)]`)
	bareReset  = regexp.MustCompile(`(resets|continuing automatically at) ([^·(]+)`)
	clockSpec  = regexp.MustCompile(`(?i)^(\d{1,2})(?::(\d{2}))? ?([ap])m$`)
	dateSpec   = regexp.MustCompile(`(?i)^([a-z]{3})[a-z]* (\d{1,2}) (\d{1,2}(?::\d{2})? ?[ap]m)$`)
	zoneinfo   = "/usr/share/zoneinfo/"
	monthIndex = map[string]time.Month{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
)

// UsageLimitReset: when does the usage limit an idle Agent stopped on reset?
// screen is its pane's screen. ok is false unless Claude's usage-limit stop is
// the last thing above the input box:
//
//	⎿  You've hit your session limit · resets 11pm (Asia/Ho_Chi_Minh)
//	● Usage limit reached · continuing automatically at 11pm · esc to cancel
//
// A bare clock is its first occurrence after since-12h: limits reset within 5h
// of the stop, and a late since (loop or Herdr restarted) still lands on it. An
// unreadable time falls back to since+5h. The zone is the Agent's, else local.
func UsageLimitReset(screen string, since int64, local *time.Location) (int64, bool) {
	lines := strings.Split(screen, "\n")
	if r := ruleLines(lines); len(r) >= 2 {
		lines = lines[:r[len(r)-2]]
	}
	i, last := len(lines)-1, ""
	for ; i >= 0; i-- {
		l := lines[i]
		// Skip blanks, turn timings, the upgrade hint and right-aligned notices.
		if skipLine.MatchString(l) || strings.Contains(l, "· done ") || strings.Contains(l, "/upgrade") {
			continue
		}
		last = l
		break
	}
	if !limitLine.MatchString(last) {
		return 0, false
	}
	spec, tz := "", ""
	for end := i - 12; i >= 0 && i > end; i-- {
		l := lines[i]
		if m := withTZ.FindStringSubmatch(l); m != nil {
			spec, tz = m[1], m[2]
			break
		} else if m := bareReset.FindStringSubmatch(l); m != nil && spec == "" {
			spec = m[2]
		}
	}
	loc := local
	if _, err := os.Stat(zoneinfo + tz); err == nil && tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	spec = strings.TrimSpace(strings.Replace(strings.ReplaceAll(spec, ",", ""), " at ", " ", -1))
	fallback := since + 18000
	at := time.Unix(since-43200, 0).In(loc)
	if m := clockSpec.FindStringSubmatch(spec); m != nil {
		t := clockOn(at.Year(), at.Month(), at.Day(), m, loc)
		if !(t.Unix() > since-43200) {
			t = clockOn(at.Year(), at.Month(), at.Day()+1, m, loc)
		}
		return t.Unix(), true
	}
	if m := dateSpec.FindStringSubmatch(spec); m != nil {
		if mon, ok := monthIndex[strings.ToLower(m[1])]; ok {
			if c := clockSpec.FindStringSubmatch(m[3]); c != nil {
				day := 0
				for _, d := range m[2] {
					day = day*10 + int(d-'0')
				}
				return clockOn(time.Unix(since, 0).In(loc).Year(), mon, day, c, loc).Unix(), true
			}
		}
	}
	return fallback, true
}

func clockOn(y int, mon time.Month, d int, m []string, loc *time.Location) time.Time {
	h, min := 0, 0
	for _, c := range m[1] {
		h = h*10 + int(c-'0')
	}
	for _, c := range m[2] {
		min = min*10 + int(c-'0')
	}
	h %= 12
	if strings.EqualFold(m[3], "p") {
		h += 12
	}
	return time.Date(y, mon, d, h, min, 0, 0, loc)
}

// InputHolds reports whether the Claude Code input box on a pane's screen
// still holds text, unsent. Claude shows pasted image paths as [Image #n] and
// long pastes as [Pasted text #n]; its idle suggestion matches neither nor
// the text.
func InputHolds(screen, text string) bool {
	lines := strings.Split(screen, "\n")
	rules := ruleLines(lines)
	if len(rules) < 2 {
		return false
	}
	box := strings.TrimPrefix(noSpace(strings.Join(lines[rules[len(rules)-2]+1:rules[len(rules)-1]], "")), "❯")
	if box == "" {
		return false
	}
	if strings.Contains(box, "[Image#") || strings.Contains(box, "[Pastedtext#") {
		return true
	}
	key := []rune(noSpace(text))
	n := min(len(key), 24)
	return n > 0 && (strings.Contains(box, string(key[:n])) || strings.Contains(box, string(key[len(key)-n:])))
}

var sgr = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// InputDraft reports whether the Claude Code input box on a pane's screen
// (`pane read --format ansi`) holds text someone typed: a draft Berthier must
// not type into. The suggested prompt Claude shows after an idle ❯ is dim
// (SGR 2); typed text is not.
func InputDraft(screen string) bool {
	lines := strings.Split(strings.ReplaceAll(screen, "\r", ""), "\n")
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = sgr.ReplaceAllString(l, "")
	}
	rules := ruleLines(plain)
	if len(rules) < 2 {
		return false
	}
	dim, prompt := false, true
	for _, l := range lines[rules[len(rules)-2]+1 : rules[len(rules)-1]] {
		for i := 0; i < len(l); {
			if m := sgr.FindStringSubmatchIndex(l[i:]); m != nil && m[0] == 0 {
				for _, p := range strings.Split(l[i+m[2]:i+m[3]], ";") {
					switch p {
					case "", "0", "22":
						dim = false
					case "2":
						dim = true
					}
				}
				i += m[1]
				continue
			}
			r, n := utf8.DecodeRuneInString(l[i:])
			i += n
			if unicode.IsSpace(r) || dim {
				continue
			}
			if prompt && r == '❯' {
				prompt = false
				continue
			}
			return true
		}
	}
	return false
}

func noSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}
