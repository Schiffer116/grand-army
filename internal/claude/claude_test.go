package claude

import (
	"slices"
	"strings"
	"testing"

	"berthier/internal/harness"
	"berthier/internal/host"
)

func TestLaunch(t *testing.T) {
	got := strings.Join(Claude{}.Launch(harness.LaunchOpts{Resume: "s1", Model: "haiku", Disallow: []string{"Artifact"}}), " ")
	if want := "claude --resume s1 --model haiku --disallowedTools Artifact"; got != want {
		t.Errorf("Launch = %q, want %q", got, want)
	}
}

// Esc interrupts a turn; Ctrl-C twice exits Claude, so only End uses it.
func TestKeysAreClaudes(t *testing.T) {
	c := Claude{}
	if got := c.Keys(harness.Interrupt); !slices.Equal(got, []host.Key{host.KeyEscape}) {
		t.Errorf("Interrupt = %v", got)
	}
	if got := c.Keys(harness.ClearDraft); !slices.Equal(got, []host.Key{host.KeyCtrlU}) {
		t.Errorf("ClearDraft = %v", got)
	}
	if got := c.Keys(harness.End); !slices.Equal(got, []host.Key{host.KeyCtrlC, host.KeyCtrlC}) {
		t.Errorf("End = %v", got)
	}
}
