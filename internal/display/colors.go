// Package display provides terminal color helpers shared across CLI commands.
// Colors are automatically disabled when stdout is not a TTY (e.g. piped to
// a file) or when the NO_COLOR environment variable is set.
package display

import (
	"fmt"

	"github.com/fatih/color"
)

// Preset colors — defined once to avoid repeated allocations.
var (
	Dim       = color.New(color.Faint)
	Bold      = color.New(color.Bold)
	BoldWhite = color.New(color.FgHiWhite, color.Bold)
	BoldCyan  = color.New(color.FgCyan, color.Bold)
	BoldGreen  = color.New(color.FgGreen, color.Bold)
	BoldYellow = color.New(color.FgYellow, color.Bold)
	Cyan       = color.New(color.FgCyan)
	Green     = color.New(color.FgGreen)
	Yellow    = color.New(color.FgYellow)
	Red       = color.New(color.FgRed)
	HiRed     = color.New(color.FgHiRed)
)

// TotalScore colors a total score out of 100.
//   ≥ 80 → bold green  |  ≥ 60 → yellow  |  else → white
func TotalScore(score float64) string {
	s := fmt.Sprintf("%.0f", score)
	switch {
	case score >= 80:
		return BoldGreen.Sprint(s)
	case score >= 60:
		return Yellow.Sprint(s)
	default:
		return s
	}
}

// Component colors a "val/max" fraction.
//   ≥ 85 % → green  |  ≥ 50 % → yellow  |  else → red
func Component(val, max float64) string {
	s := fmt.Sprintf("%.0f/%.0f", val, max)
	frac := val / max
	switch {
	case frac >= 0.85:
		return Green.Sprint(s)
	case frac >= 0.50:
		return Yellow.Sprint(s)
	default:
		return Red.Sprint(s)
	}
}

// ComponentF is like Component but formats with one decimal place (for cmd/scan).
func ComponentF(val, max float64) string {
	s := fmt.Sprintf("%.1f / %.0f", val, max)
	frac := val / max
	switch {
	case frac >= 0.85:
		return Green.Sprint(s)
	case frac >= 0.50:
		return Yellow.Sprint(s)
	default:
		return Red.Sprint(s)
	}
}

// Quality colors a trade quality label.
func Quality(q string) string {
	switch q {
	case "excellent":
		return BoldGreen.Sprint(q)
	case "good":
		return Green.Sprint(q)
	case "fair":
		return Yellow.Sprint(q)
	default:
		return Red.Sprint(q)
	}
}

// Trend colors a trend string.
func Trend(t string) string {
	switch t {
	case "bullish":
		return Green.Sprint(t)
	case "bearish":
		return Red.Sprint(t)
	default:
		return Yellow.Sprint(t)
	}
}

// Sign colors a signed percentage value: positive → green, negative → red.
// format should include the sign verb, e.g. "%+.2f%%".
func Sign(val float64, format string) string {
	s := fmt.Sprintf(format, val)
	switch {
	case val > 0:
		return Green.Sprint(s)
	case val < 0:
		return Red.Sprint(s)
	default:
		return s
	}
}

// RR colors a risk/reward ratio by quality tier.
func RR(rr float64) string {
	s := fmt.Sprintf("%.2f", rr)
	switch {
	case rr >= 3.0:
		return BoldGreen.Sprint(s)
	case rr >= 2.0:
		return Green.Sprint(s)
	default:
		return Yellow.Sprint(s)
	}
}
