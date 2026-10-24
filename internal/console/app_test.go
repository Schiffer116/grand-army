package console

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func get(t *testing.T, url string) (*http.Response, []byte) {
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("GET %s = %d", url, res.StatusCode)
	}
	return res, body
}

// The page links a manifest whose icons are all served at their stated size,
// and every file the service worker keeps exists.
func TestInstallable(t *testing.T) {
	srv := httptest.NewServer(Handler(NewArmy(), Ports{}, nil))
	defer srv.Close()

	_, page := get(t, srv.URL+"/")
	for _, want := range []string{`rel="manifest" href="manifest.webmanifest"`, `rel="apple-touch-icon" href="icon-180.png"`,
		`viewport-fit=cover`, `register('sw.js')`} {
		if !bytes.Contains(page, []byte(want)) {
			t.Errorf("page lacks %s", want)
		}
	}

	res, body := get(t, srv.URL+"/manifest.webmanifest")
	if ct := res.Header.Get("Content-Type"); ct != "application/manifest+json" {
		t.Errorf("manifest Content-Type %q", ct)
	}
	var m struct {
		Name, Display string
		StartURL      string `json:"start_url"`
		ThemeColor    string `json:"theme_color"`
		Icons         []struct{ Src, Sizes, Type, Purpose string }
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "Berthier" || m.Display != "standalone" || m.StartURL == "" {
		t.Errorf("manifest name %q display %q start_url %q", m.Name, m.Display, m.StartURL)
	}
	if m.ThemeColor == "" || !bytes.Contains(page, []byte(`name="theme-color" content="`+m.ThemeColor+`"`)) {
		t.Errorf("manifest theme_color %q differs from the page's", m.ThemeColor)
	}
	srcs := map[string]bool{"icon-180.png": true}
	for _, ic := range m.Icons {
		srcs[ic.Src] = true
		var w, h int
		if _, err := fmt.Sscanf(ic.Sizes, "%dx%d", &w, &h); err != nil {
			t.Fatalf("icon sizes %q", ic.Sizes)
		}
		_, img := get(t, srv.URL+"/"+ic.Src)
		cfg, err := png.DecodeConfig(bytes.NewReader(img))
		if err != nil || cfg.Width != w || cfg.Height != h {
			t.Errorf("%s: %dx%d (%v), manifest says %s", ic.Src, cfg.Width, cfg.Height, err, ic.Sizes)
		}
	}
	if !srcs["icon-192.png"] || !srcs["icon-512.png"] {
		t.Errorf("manifest icons %v lack 192 or 512", srcs)
	}

	res, sw := get(t, srv.URL+"/sw.js")
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("sw.js Content-Type %q", ct)
	}
	shell := regexp.MustCompile(`SHELL = \[([^\]]*)\]`).FindSubmatch(sw)
	if shell == nil {
		t.Fatal("sw.js has no SHELL list")
	}
	for _, p := range regexp.MustCompile(`'([^']*)'`).FindAllSubmatch(shell[1], -1) {
		if path := strings.TrimPrefix(string(p[1]), "./"); strings.HasPrefix(path, "api/") {
			t.Errorf("sw.js caches %s", p[1])
		} else {
			get(t, srv.URL+"/"+path)
		}
	}
}

// The icon is the imperial eagle in the accent and peach on the page
// background, wings inside the maskable safe zone.
func TestIconDrawsEagle(t *testing.T) {
	t.Setenv("BERTHIER_THEME", filepath.Join(t.TempDir(), "none.css"))
	const n = 512
	img, err := png.Decode(bytes.NewReader(Icon(n)))
	if err != nil || img.Bounds().Dx() != n {
		t.Fatalf("decode: %v %v", err, img.Bounds())
	}
	bg := themeRGBA("bg")
	if r, g, b, _ := img.At(2, 2).RGBA(); uint8(r>>8) != bg.R || uint8(g>>8) != bg.G || uint8(b>>8) != bg.B {
		t.Errorf("corner is not the background: %d %d %d", r>>8, g>>8, b>>8)
	}
	ink := 0
	for y := range n {
		for x := range n {
			r, _, _, _ := img.At(x, y).RGBA()
			if uint8(r>>8) == bg.R {
				continue
			}
			ink++
			dx, dy := float64(x)/n-0.5, float64(y)/n-0.5
			if dx*dx+dy*dy > 0.41*0.41 {
				t.Fatalf("ink at %d,%d outside the safe zone", x, y)
			}
		}
	}
	if ink < n*n/10 {
		t.Errorf("only %d ink pixels", ink)
	}
}

// /theme.css is the dotfiles palette when present, else the embedded copy;
// served pages carry its colours and no stray hex.
func TestThemeServedFromPalette(t *testing.T) {
	path := filepath.Join(t.TempDir(), "palette.css")
	t.Setenv("BERTHIER_THEME", path)
	srv := httptest.NewServer(Handler(NewArmy(), Ports{}, nil))
	defer srv.Close()

	res, css := get(t, srv.URL+"/theme.css")
	if !bytes.Equal(css, themeFallback) || res.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("missing palette: want fallback, no-cache; got %q", res.Header.Get("Cache-Control"))
	}
	os.WriteFile(path, []byte(":root { --base: #010203; --crust: #040506; }"), 0o644)
	if _, css = get(t, srv.URL+"/theme.css"); !strings.Contains(string(css), "#010203") {
		t.Errorf("palette not served: %s", css)
	}
	if _, page := get(t, srv.URL+"/"); !strings.Contains(string(page), `content="#010203"`) || strings.Contains(string(page), "{{") {
		t.Error("theme-color not filled from palette")
	}
	if _, m := get(t, srv.URL+"/manifest.webmanifest"); !strings.Contains(string(m), "#040506") {
		t.Errorf("manifest colours: %s", m)
	}
	if _, svg := get(t, srv.URL+"/icon.svg"); !strings.Contains(string(svg), "fill=\"#b4befe\"") || strings.Contains(string(svg), "{{") {
		t.Error("icon.svg not themed")
	}
	if hex := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`).FindString(string(indexHTML)); hex != "" {
		t.Errorf("index.html has raw hex %s", hex)
	}
}
