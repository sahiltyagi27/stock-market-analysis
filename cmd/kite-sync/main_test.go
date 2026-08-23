package main

import (
	"testing"
	"time"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestChunkWindows_FitsInOneWindow(t *testing.T) {
	from, to := day("2024-01-01"), day("2024-06-01")
	windows := chunkWindows(from, to)
	if len(windows) != 1 {
		t.Fatalf("expected 1 window for a short range, got %d", len(windows))
	}
	if !windows[0][0].Equal(from) || !windows[0][1].Equal(to) {
		t.Fatalf("expected window %v-%v, got %v-%v", from, to, windows[0][0], windows[0][1])
	}
}

func TestChunkWindows_SplitsWideRange(t *testing.T) {
	// ~16.6 years, well beyond maxChunkDays (1900 ≈ 5.2y) — must split.
	from, to := day("2010-01-01"), day("2026-08-23")
	windows := chunkWindows(from, to)
	if len(windows) < 2 {
		t.Fatalf("expected multiple windows for a 16+ year range, got %d", len(windows))
	}

	// Windows must be contiguous (no gaps, no overlaps) and cover [from, to)
	// exactly.
	if !windows[0][0].Equal(from) {
		t.Fatalf("first window must start at %v, got %v", from, windows[0][0])
	}
	if !windows[len(windows)-1][1].Equal(to) {
		t.Fatalf("last window must end at %v, got %v", to, windows[len(windows)-1][1])
	}
	for i := 1; i < len(windows); i++ {
		if !windows[i-1][1].Equal(windows[i][0]) {
			t.Fatalf("window %d ends at %v but window %d starts at %v — gap or overlap",
				i-1, windows[i-1][1], i, windows[i][0])
		}
	}
	for i, w := range windows {
		if w[1].Sub(w[0]) > maxChunkDays*24*time.Hour {
			t.Fatalf("window %d spans %v, wider than maxChunkDays", i, w[1].Sub(w[0]))
		}
	}
}

func TestChunkWindows_ExactlyAtLimit(t *testing.T) {
	from := day("2020-01-01")
	to := from.AddDate(0, 0, maxChunkDays)
	windows := chunkWindows(from, to)
	if len(windows) != 1 {
		t.Fatalf("a range exactly at maxChunkDays should still be one window, got %d", len(windows))
	}
}

func TestChunkWindows_ZeroLengthRange(t *testing.T) {
	from := day("2024-01-01")
	windows := chunkWindows(from, from)
	if len(windows) != 1 {
		t.Fatalf("a zero-length range should return one (empty) window, got %d", len(windows))
	}
	if !windows[0][0].Equal(from) || !windows[0][1].Equal(from) {
		t.Fatalf("expected the same from==to window back, got %v-%v", windows[0][0], windows[0][1])
	}
}
