package hosttest

import (
	"testing"

	"berthier/internal/host"
)

func TestFakeContract(t *testing.T) {
	Contract(t, func(t *testing.T) host.Host {
		f := New()
		f.Exec = true
		return f
	})
}

func TestFakeDown(t *testing.T) {
	f := New()
	f.Down = true
	if _, err := f.Snapshot(); err != host.ErrUnreachable {
		t.Errorf("Snapshot on a down Host: %v", err)
	}
}
