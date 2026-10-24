package app

import (
	"bytes"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"berthier/internal/core"
)

// Loop is the Main Loop: one tick every BERTHIER_INTERVAL seconds, forever. A
// failed tick is retried next interval and never kills the loop; failures are
// logged once per streak, so a long Herdr outage is two lines, not thousands.
func (e *Env) Loop() {
	// loop.log used to be prose: set it aside once, so the log is all JSON Lines.
	if b, err := os.ReadFile(e.Log); err == nil && hasProse(b) {
		os.Rename(e.Log, e.Log+".pre-json")
	}
	session := e.HerdrSession
	if session == "" {
		session = "default"
	}
	e.Event("loop_started", fmt.Sprintf("loop started (interval %ss, session %s)", e.Interval, session),
		core.Fields{"interval": e.IntervalSecs, "session": session})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	stopped := func() {
		e.Event("loop_stopped", "loop stopped", nil)
		os.Exit(0)
	}
	failing := ""
	for {
		why := e.safeTick()
		if why == "" {
			if failing != "" {
				e.Event("loop_recovered", "loop recovered after: "+failing, core.Fields{"why": failing})
			}
			failing = ""
		} else {
			if why != failing {
				e.Event("loop_error", fmt.Sprintf("error: %s; retrying every %ss", why, e.Interval), core.Fields{"why": why})
			}
			failing = why
		}
		// A signal during a tick is handled once the tick is done.
		select {
		case <-sig:
			stopped()
		case <-time.After(time.Duration(e.IntervalSecs * float64(time.Second))):
		}
	}
}

// safeTick runs a tick; why is empty when it succeeded, else a streak key.
func (e *Env) safeTick() (why string) {
	defer func() {
		if r := recover(); r != nil {
			why = "tick failed (exit 1)"
		}
	}()
	switch err := e.Tick(); {
	case err == nil:
		return ""
	case err == ErrHostUnreachable:
		return "host unreachable"
	default:
		return "tick failed (exit 1)"
	}
}

func hasProse(b []byte) bool {
	for _, l := range bytes.Split(b, []byte("\n")) {
		if len(l) > 0 && l[0] != '{' {
			return true
		}
	}
	return false
}
