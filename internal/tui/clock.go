package tui

import "time"

// Clock abstracts time for deterministic tests.
type Clock interface {
	Now() time.Time
}

// realClock uses time.Now.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// fakeClock is a controllable clock for tests.
type fakeClock struct {
	t time.Time
}

func (f *fakeClock) Now() time.Time { return f.t }
func (f *fakeClock) Advance(d time.Duration) { f.t = f.t.Add(d) }

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{t: t} }
