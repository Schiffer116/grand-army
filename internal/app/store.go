package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"berthier/internal/core"
)

// Event appends one event to loop.log, JSON Lines: {ts, kind, <fields>, msg}
// where fields are whichever of pane, corps, name, session, to, why, ... the
// event has. msg is the English line; readers match on kind and fields, never
// msg. `berthier log` pretty-prints it; core.Army labels each event for the
// Console. One O_APPEND write, so concurrent writers never interleave.
func (e *Env) Event(kind, msg string, fields core.Fields) {
	line := eventLine(e.now(), kind, msg, fields)
	f, err := os.OpenFile(e.Log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(line)
}

func eventLine(ts int64, kind, msg string, fields core.Fields) []byte {
	var b bytes.Buffer
	enc := func(v any) []byte {
		x, _ := json.Marshal(v)
		return x
	}
	fmt.Fprintf(&b, `{"ts":%d,"kind":%s`, ts, enc(kind))
	keys := make([]string, 0, len(fields))
	for k := range fields {
		if k != "ts" && k != "kind" && k != "msg" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, `,%s:%s`, enc(k), enc(fields[k]))
	}
	fmt.Fprintf(&b, `,"msg":%s}`+"\n", enc(msg))
	return b.Bytes()
}

// readJSON decodes a file; a missing or malformed one leaves v untouched.
func readJSON(path string, v any) bool {
	b, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(b, v) == nil
}

// writeAtomic replaces path with data, so a reader never sees half a file.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o644)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		os.Remove(tmp.Name())
	}
	return werr
}

func writeJSONAtomic(path string, v any, indent bool) error {
	var b []byte
	var err error
	if indent {
		b, err = json.MarshalIndent(v, "", "  ")
	} else {
		b, err = json.Marshal(v)
	}
	if err != nil {
		return err
	}
	return writeAtomic(path, append(b, '\n'))
}

// ReadState is the Grand Army State; zero before the Main Loop's first tick.
func (e *Env) ReadState() core.State {
	var s core.State
	readJSON(e.State, &s)
	return s
}

// ReadDivisions is state/divisions.json: {name: {corps, worktree, model?}}.
func (e *Env) ReadDivisions() core.Divisions {
	d := core.Divisions{}
	readJSON(e.Divisions, &d)
	return d
}

// SetDivision records a Division's Corps in divisions.json; nil forgets it.
// Atomic, so the Main Loop never reads half a file.
func (e *Env) SetDivision(name string, d *core.Division) error {
	m := map[string]*core.Division{}
	readJSON(e.Divisions, &m)
	if d == nil {
		delete(m, name)
	} else {
		m[name] = d
	}
	return writeJSONAtomic(e.Divisions, m, true)
}

// ModelOf is the model recorded for the Division labelled label (empty:
// none, claude's default).
func (e *Env) ModelOf(label string) string {
	return e.ReadDivisions()[core.CorpsName(label)].Model
}

// ReadBudgets is state/budgets.json: {corps: % of the week}.
func (e *Env) ReadBudgets() map[string]float64 {
	b := map[string]float64{}
	readJSON(e.Budgets, &b)
	return b
}

func fileID(name string) string { return strings.TrimSuffix(name, ".json") }

// ReadInbox lists pending asks, each with its file name as id.
func (e *Env) ReadInbox() []core.Ask {
	asks := []core.Ask{}
	for _, f := range jsonFiles(e.Inbox) {
		var a core.Ask
		if readJSON(f, &a) {
			a.ID = fileID(filepath.Base(f))
			asks = append(asks, a)
		}
	}
	return asks
}

// ReadReports is the Agents' reports by Claude session id.
func (e *Env) ReadReports() map[string]core.Report {
	r := map[string]core.Report{}
	for _, f := range jsonFiles(e.Reports) {
		var x core.Report
		if readJSON(f, &x) {
			r[fileID(filepath.Base(f))] = x
		}
	}
	return r
}

// ReadDemos is state/demos: session id -> the demos that Agent reported.
func (e *Env) ReadDemos() map[string][]core.Demo {
	r := map[string][]core.Demo{}
	for _, f := range jsonFiles(e.Demos) {
		var x []core.Demo
		if readJSON(f, &x) {
			r[fileID(filepath.Base(f))] = x
		}
	}
	return r
}

func jsonFiles(dir string) []string {
	m, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	slices.Sort(m)
	return m
}

// TailLines returns the last n lines of a file.
func TailLines(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	size, _ := f.Seek(0, io.SeekEnd)
	var buf []byte
	const chunk = 64 << 10
	for off := size; off > 0 && bytes.Count(buf, []byte("\n")) <= n; {
		step := int64(chunk)
		if off < step {
			step = off
		}
		off -= step
		part := make([]byte, step)
		f.ReadAt(part, off)
		buf = append(part, buf...)
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

// lockFile takes an exclusive flock on path; call the result to release it.
// With wait false it returns nil when someone else holds the lock.
func lockFile(path string, wait bool) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, nil
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}

// UpdateUsage folds the Profile (UpdateProfile) and then the ledger rows
// added since last time into state/usage.json (core.Usage.Fold, each rise in
// week_pct split by transcript Spend) and returns the record. Locked, so the
// Main Loop and `berthier budget` never fold the same rows twice. A record
// from before transcripts were the source is refolded from the whole ledger
// once. A ledger past BERTHIER_LEDGER_MAX is moved aside once folded: new
// samples start a fresh file.
func (e *Env) UpdateUsage() (core.Usage, error) {
	e.UpdateProfile() // its errors are the profile command's to report
	unlock, err := lockFile(filepath.Join(e.Home, "usage.lock"), true)
	if err != nil {
		return core.Usage{}, err
	}
	defer unlock()
	var u core.Usage
	if b, err := os.ReadFile(e.Usage); err == nil && len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &u); err != nil {
			return u, err
		}
	}
	var size int64
	if st, err := os.Stat(e.Ledger); err == nil {
		size = st.Size()
	}
	var rows []core.Row
	offset := u.Offset
	if u.Source != core.UsageSource {
		u, offset = core.Usage{}, 0
		if st, err := os.Stat(e.Ledger + ".prev"); err == nil {
			rows = ledgerRows(e.Ledger+".prev", 0, st.Size())
		}
	}
	if offset > size { // the ledger was replaced
		offset = 0
	}
	if size > offset || u.Source != core.UsageSource {
		rows = append(rows, ledgerRows(e.Ledger, offset, size)...)
		moved := false
		if size > e.LedgerMax {
			if os.Rename(e.Ledger, e.Ledger+".prev") == nil {
				moved = true
			}
		}
		next := size
		if moved {
			next = 0
		}
		u = u.Fold(rows, e.SpendSeries(), next)
		if err := writeAtomic(e.Usage, mustJSON(u)); err != nil {
			return u, err
		}
	}
	cutoff := time.Now().Add(-14 * 24 * time.Hour)
	filepath.WalkDir(filepath.Join(e.Home, "sampled"), func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil && info.ModTime().Before(cutoff) {
				os.Remove(p)
			}
		}
		return nil
	})
	return u, nil
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return append(b, '\n')
}

// ledgerRows reads the ledger's bytes [offset, size) as rows; lines that are
// not JSON objects are skipped.
func ledgerRows(path string, offset, size int64) []core.Row {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var rows []core.Row
	sc := bufio.NewScanner(io.NewSectionReader(f, offset, size-offset))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		var r core.Row
		if len(line) > 0 && line[0] == '{' && json.Unmarshal(line, &r) == nil {
			rows = append(rows, r)
		}
	}
	return rows
}

// stdioReadAll reads a command's input (a hook's JSON) whole; a failed read
// leaves what came.
func stdioReadAll(r io.Reader) ([]byte, error) { return io.ReadAll(r) }
