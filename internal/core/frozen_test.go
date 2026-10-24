package core

import (
	"strings"
	"testing"
)

func TestCache(t *testing.T) {
	c := Cache{LastAt: 1792749605, Context: 152503, Model: "claude-opus-5-5", TTL1h: true}
	usd, _ := c.ReviveUSD()
	check(t, "a Revive writes the context at the 1h rate", round2(usd), `1.22`)
	c.TTL1h = false
	usd, _ = c.ReviveUSD()
	check(t, "or the 5m one", round2(usd), `0.76`)
	usd, _ = c.WriteUSD(60000)
	check(t, "a Fresh Start's estimate priced the same way", round2(usd), `0.30`)
	if _, ok := (Cache{Model: "gpt-9"}).WriteUSD(1); ok {
		t.Error("an unpriced model priced")
	}
	if _, ok := (Cache{Model: "gpt-9"}).ReviveUSD(); ok {
		t.Error("an unpriced model priced")
	}
	cfg := Config{CacheWarm: 3300, FrozenMin: 50000}
	check(t, "warm, small, cold", []bool{Cache{LastAt: 1000, Context: 90000}.Warm(4299, cfg), Cache{Context: 49999}.Warm(1e9, cfg), Cache{LastAt: 1000, Context: 90000}.Warm(4300, cfg)}, `[true,true,false]`)
}

func TestFreshPrompt(t *testing.T) {
	p := FreshPrompt("halfway", "Build it.")
	if OriginOf(p) != "fresh" || !strings.Contains(p, "run berthier notes, read your Board, your last report and git status") ||
		!strings.Contains(p, "Your last report: halfway") || !strings.HasSuffix(p, "Your original Brief:\n\nBuild it.") {
		t.Errorf("fresh prompt: %q", p)
	}
	if p := FreshPrompt("", ""); strings.Contains(p, "\n") {
		t.Errorf("a Marshal's fresh prompt: %q", p)
	}
}
