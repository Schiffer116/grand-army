package app

import (
	"bufio"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// unitSmoke starts what a unit's ExecStart runs, against a fake Herdr and a
// scratch home, and says whether it came up: a unit whose command never
// starts (e.g. an unregistered command) restarts forever unnoticed.
var unitSmoke = map[string]struct {
	args []string
	up   func(t *testing.T, home string, stderr io.Reader) bool
}{
	"dashboard": {[]string{"-addr", "127.0.0.1:0"}, func(t *testing.T, home string, stderr io.Reader) bool {
		addr := regexp.MustCompile(`on (http://\S+)`)
		lines := bufio.NewScanner(stderr)
		for lines.Scan() {
			if m := addr.FindStringSubmatch(lines.Text()); m != nil {
				resp, err := http.Get(m[1] + "/")
				if err != nil {
					t.Log(err)
					return false
				}
				resp.Body.Close()
				return resp.StatusCode == 200
			}
			t.Log(lines.Text())
		}
		return false
	}},
	"loop": {nil, func(t *testing.T, home string, stderr io.Reader) bool {
		go io.Copy(io.Discard, stderr)
		for range 100 {
			if _, err := os.Stat(filepath.Join(home, "state.json")); err == nil {
				return true
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	}},
}

func TestSystemdUnitsStart(t *testing.T) {
	units, _ := filepath.Glob("../../systemd/berthier-*.service")
	if len(units) == 0 {
		t.Fatal("no units found")
	}
	bin := filepath.Join(t.TempDir(), "berthier")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/berthier").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	execStart := regexp.MustCompile(`(?m)^ExecStart=\S*/bin/berthier(.*)$`)
	for _, u := range units {
		b, _ := os.ReadFile(u)
		m := execStart.FindStringSubmatch(string(b))
		if m == nil {
			t.Errorf("%s: no ExecStart running bin/berthier", u)
			continue
		}
		args := strings.Fields(m[1])
		if len(args) == 0 {
			t.Errorf("%s: ExecStart runs berthier without a command", u)
			continue
		}
		smoke, ok := unitSmoke[args[0]]
		if !ok {
			t.Errorf("%s: no smoke check for %q; add one to unitSmoke", u, args[0])
			continue
		}
		t.Run(filepath.Base(u), func(t *testing.T) {
			s := newSandbox(t)
			src, err := os.ReadFile("../../tests/fake-herdr")
			if err != nil {
				t.Fatal(err)
			}
			herdr := filepath.Join(t.TempDir(), "herdr")
			os.WriteFile(herdr, src, 0o755)
			cmd := exec.Command(bin, append(args, smoke.args...)...)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + s.home, "BERTHIER_HOME=" + s.home,
				"BERTHIER_ROOT=" + s.home, "BERTHIER_HERDR=" + herdr, "BERTHIER_INTERVAL=0.1",
				"BERTHIER_SPEAK=true", "CLAUDE_CONFIG_DIR=" + filepath.Join(s.home, "claude")}
			stderr, _ := cmd.StderrPipe()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { cmd.Process.Kill(); cmd.Wait() }()
			if !smoke.up(t, s.home, stderr) {
				t.Errorf("berthier %s did not come up", strings.Join(args, " "))
			}
		})
	}
}
