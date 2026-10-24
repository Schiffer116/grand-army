package app

import (
	"bytes"
	"cmp"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"

	"berthier/internal/core"
)

// The Profile index (ADR 0005): state/profile/ holds
//   cursor.json   how far each transcript has been read, and its fold state
//   turns.jsonl   one row per turn and model per pass (core.Turn; same Key merges)
//   archive.json  per-session totals of weeks older than the kept ones
// UpdateProfile folds what Claude Code appended to its transcripts since the
// last pass; LoadTurns reads the index back, merged.

type profileMeta struct {
	Version int                    `json:"version"`
	Oldest  float64                `json:"oldest"` // earliest turn End in turns.jsonl
	Files   map[string]core.Cursor `json:"files"`
}

func (e *Env) profileDir() string { return filepath.Join(e.Home, "profile") }

// profileSince is where the kept weeks start: the current quota week and the
// previous four (BERTHIER_PROFILE_WEEKS in all), never before BERTHIER_PROFILE_SINCE.
func (e *Env) profileSince(now int64) float64 {
	var u core.Usage
	readJSON(e.Usage, &u)
	start := core.WeekStart(float64(now), u.ResetsAt)
	since := start - float64(e.ProfileWeeks-1)*core.Week
	return max(since, e.ProfileSince)
}

// allTranscripts lists Claude Code's transcripts: main sessions and subagents.
func (e *Env) allTranscripts() []string {
	root := filepath.Join(e.ClaudeDir, "projects")
	a, _ := filepath.Glob(filepath.Join(root, e.ProfileProjects, "*.jsonl"))
	b, _ := filepath.Glob(filepath.Join(root, e.ProfileProjects, "*", "subagents", "*.jsonl"))
	return append(a, b...)
}

func transcriptFile(path string) core.TranscriptFile {
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if filepath.Base(filepath.Dir(path)) == "subagents" {
		parent := filepath.Base(filepath.Dir(filepath.Dir(path)))
		return core.TranscriptFile{Session: parent + "/" + id, Parent: parent}
	}
	return core.TranscriptFile{Session: id}
}

// UpdateProfile folds new transcript lines into the Profile index. Locked, so
// the Main Loop and the commands never fold the same lines twice.
func (e *Env) UpdateProfile() error {
	if err := os.MkdirAll(e.profileDir(), 0o755); err != nil {
		return err
	}
	unlock, err := lockFile(filepath.Join(e.profileDir(), "lock"), true)
	if err != nil {
		return err
	}
	defer unlock()
	now := e.now()
	since := e.profileSince(now)
	meta := profileMeta{Files: map[string]core.Cursor{}}
	readJSON(filepath.Join(e.profileDir(), "cursor.json"), &meta)
	if meta.Files == nil || meta.Version != profileVersion {
		meta = profileMeta{Version: profileVersion, Files: map[string]core.Cursor{}}
		os.Remove(filepath.Join(e.profileDir(), "turns.jsonl"))
	}
	st := e.ReadState()
	placer := core.NewPlacer(&st, e.ReadDivisions(), e.Root)
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	changed := false
	seen := map[string]bool{}
	for _, path := range e.allTranscripts() {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if float64(info.ModTime().Unix()) < since {
			continue
		}
		seen[path] = true
		c := meta.Files[path]
		size, mtime := info.Size(), info.ModTime().UnixNano()
		if size == c.Size && mtime == c.MTime {
			continue
		}
		if size < c.Offset { // rewritten: start over
			c = core.Cursor{}
		}
		lines, next := readLines(path, c.Offset, size)
		c, rows := core.FoldTranscript(c, transcriptFile(path), lines, placer.Place)
		c.Offset, c.Size, c.MTime = next, size, mtime
		if float64(now)-float64(info.ModTime().Unix()) > 86400 {
			c.Pending, c.Reads = nil, nil // an idle session: re-reading it later is no waste
		}
		meta.Files[path] = c
		changed = true
		for _, r := range rows {
			if r.End < since {
				continue
			}
			if meta.Oldest == 0 || r.End < meta.Oldest {
				meta.Oldest = r.End
			}
			enc.Encode(r)
		}
	}
	for p := range meta.Files {
		if !seen[p] {
			delete(meta.Files, p)
			changed = true
		}
	}
	turns := filepath.Join(e.profileDir(), "turns.jsonl")
	if out.Len() > 0 {
		f, err := os.OpenFile(turns, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		_, err = f.Write(out.Bytes())
		f.Close()
		if err != nil {
			return err
		}
	}
	if meta.Oldest > 0 && meta.Oldest < since {
		if err := e.compactProfile(since); err != nil {
			return err
		}
		meta.Oldest = since
		changed = true
	}
	if changed {
		return writeJSONAtomic(filepath.Join(e.profileDir(), "cursor.json"), meta, false)
	}
	return nil
}

// profileVersion changes when the fold changes what it writes: the index is
// then rebuilt from the transcripts.
const profileVersion = 4

// readLines reads [offset, size) of a file as complete lines; next is the
// offset after the last newline, so a line still being written waits.
func readLines(path string, offset, size int64) (lines [][]byte, next int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset
	}
	defer f.Close()
	buf := make([]byte, size-offset)
	n, _ := io.ReadFull(io.NewSectionReader(f, offset, size-offset), buf)
	buf = buf[:n]
	end := bytes.LastIndexByte(buf, '\n')
	if end < 0 {
		return nil, offset
	}
	for _, l := range bytes.Split(buf[:end], []byte("\n")) {
		if len(l) > 0 && l[0] == '{' {
			lines = append(lines, l)
		}
	}
	return lines, offset + int64(end) + 1
}

// compactProfile moves turns that ended before since into archive.json's
// per-session totals; the rest stay as they are, part by part, so the quota
// attribution keeps their timing.
func (e *Env) compactProfile(since float64) error {
	path := filepath.Join(e.profileDir(), "turns.jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	archive := e.ReadArchive()
	var kept bytes.Buffer
	for _, line := range bytes.Split(b, []byte("\n")) {
		var t core.Turn
		if json.Unmarshal(line, &t) != nil {
			continue
		}
		if t.End < since {
			archive.Add(t)
		} else {
			kept.Write(line)
			kept.WriteByte('\n')
		}
	}
	if err := writeJSONAtomic(filepath.Join(e.profileDir(), "archive.json"), archive, false); err != nil {
		return err
	}
	return writeAtomic(path, kept.Bytes())
}

// ReadArchive is the per-session totals of compacted weeks.
func (e *Env) ReadArchive() core.Archive {
	a := core.Archive{}
	readJSON(filepath.Join(e.profileDir(), "archive.json"), &a)
	return a
}

// turnCache keeps the index in a long-running process (the loop, the
// Console) and reads only what was appended since: the turns merged (one per
// turn and model), and every row's Spend as it was folded.
type turnCache struct {
	mu     sync.Mutex
	ino    uint64
	offset int64
	turns  []core.Turn
	index  map[string]int
	spend  []core.SpendAt
}

var turnIndex turnCache

// loadIndex brings the cache up to date with turns.jsonl.
func (e *Env) loadIndex() {
	path := filepath.Join(e.profileDir(), "turns.jsonl")
	c := &turnIndex
	f, err := os.Open(path)
	if err != nil {
		c.ino, c.offset, c.turns, c.index, c.spend = 0, 0, nil, nil, nil
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return
	}
	if ino := inode(info); ino != c.ino || info.Size() < c.offset || c.index == nil {
		c.ino, c.offset, c.turns, c.index, c.spend = ino, 0, nil, map[string]int{}, nil
	}
	if info.Size() <= c.offset {
		return
	}
	buf := make([]byte, info.Size()-c.offset)
	n, _ := io.ReadFull(io.NewSectionReader(f, c.offset, int64(len(buf))), buf)
	buf = buf[:n]
	end := bytes.LastIndexByte(buf, '\n')
	if end < 0 {
		return
	}
	for _, line := range bytes.Split(buf[:end], []byte("\n")) {
		var t core.Turn
		if json.Unmarshal(line, &t) != nil {
			continue
		}
		c.spend = append(c.spend, core.SpendAt{Ts: t.End, Corps: t.Corps, USD: t.USD})
		if i, ok := c.index[t.Key()]; ok {
			c.turns[i].Merge(t)
		} else {
			c.index[t.Key()] = len(c.turns)
			c.turns = append(c.turns, t)
		}
	}
	c.offset += int64(end) + 1
}

// LoadTurns is the Profile index, merged: one Turn per turn and model, by start.
func (e *Env) LoadTurns() []core.Turn {
	turnIndex.mu.Lock()
	defer turnIndex.mu.Unlock()
	e.loadIndex()
	out := make([]core.Turn, len(turnIndex.turns))
	for i, t := range turnIndex.turns {
		t.Rereads = append([]string(nil), t.Rereads...)
		out[i] = t
	}
	slices.SortStableFunc(out, func(a, b core.Turn) int { return cmp.Compare(a.Start, b.Start) })
	return out
}

// SpendSeries is every Profile row's Spend by Corps, by time: what the quota
// attribution splits each rise in week_pct by.
func (e *Env) SpendSeries() []core.SpendAt {
	turnIndex.mu.Lock()
	defer turnIndex.mu.Unlock()
	e.loadIndex()
	out := append([]core.SpendAt(nil), turnIndex.spend...)
	slices.SortStableFunc(out, func(a, b core.SpendAt) int { return cmp.Compare(a.Ts, b.Ts) })
	return out
}

func inode(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}
