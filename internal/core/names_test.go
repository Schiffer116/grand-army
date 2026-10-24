package core

import "testing"

func TestSlugAndNames(t *testing.T) {
	for in, want := range map[string]string{"Dark Mode": "dark-mode", "demo-dark-mode": "demo-dark-mode", "9 Lives!": "lives", ".dot files": "dot-files",
		"abcdefghijklmnopqrstuvwxyzabcdefghij": "abcdefghijklmnopqrstuvwxyzabcdef"} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
	if CorpsName(".Dotfiles") != "dotfiles" || Spoken(".dark-mode_x") != "dark mode x" || FallbackName("W1:P1") != "a-w1-p1" {
		t.Error("names")
	}
}
