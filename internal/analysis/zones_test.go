package analysis_test

import (
	"testing"

	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
)

var defaultOpts = analysis.ZoneOptions{Window: 2, ClusterPct: 0.02}

// ---------------------------------------------------------------------------
// Local extrema helpers (tested indirectly via FindZones)
// ---------------------------------------------------------------------------

func TestFindZones_NoData(t *testing.T) {
	r := analysis.FindZones([]float64{}, []float64{}, defaultOpts)
	if len(r.Support) != 0 || len(r.Resistance) != 0 {
		t.Errorf("expected empty zones for empty input, got %+v", r)
	}
}

func TestFindZones_TooShortForWindow(t *testing.T) {
	// With window=2 we need at least 5 candles to find any interior extreme.
	prices := []float64{10, 8, 9}
	r := analysis.FindZones(prices, prices, defaultOpts)
	if len(r.Support) != 0 || len(r.Resistance) != 0 {
		t.Errorf("expected no zones for 3-candle series, got %+v", r)
	}
}

// ---------------------------------------------------------------------------
// Support zones
// ---------------------------------------------------------------------------

func TestFindZones_SingleClearLow(t *testing.T) {
	// One obvious local low at index 3.
	lows := []float64{50, 45, 42, 30, 43, 46, 50, 48, 51}
	highs := []float64{55, 50, 47, 35, 48, 51, 55, 53, 56}
	r := analysis.FindZones(highs, lows, defaultOpts)

	if len(r.Support) == 0 {
		t.Fatal("expected at least one support zone")
	}
	// The zone must contain the low value 30.
	found := false
	for _, z := range r.Support {
		if z.Low <= 30 && z.High >= 30 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected support zone around 30, got %+v", r.Support)
	}
}

func TestFindZones_MultipleDistinctLows(t *testing.T) {
	// Two clearly separated V-shapes: one bottoming near 20, one near 80.
	// Each V is self-contained so the local min is unambiguous.
	lows := []float64{
		100, 60, 20, 60, 100, // V-bottom at 20
		100, 60, 21, 60, 100, // second touch near 20
		100, 140, 80, 140, 100, // V-bottom at 80
		100, 140, 81, 140, 100, // second touch near 80
	}
	highs := make([]float64, len(lows))
	for i, v := range lows {
		highs[i] = v + 10
	}

	r := analysis.FindZones(highs, lows, defaultOpts)
	if len(r.Support) < 2 {
		t.Errorf("expected ≥2 support zones for two distinct low clusters, got %d: %+v", len(r.Support), r.Support)
	}
}

func TestFindZones_ClusteredLowsMerge(t *testing.T) {
	// Two lows that are within 2% of each other should merge into one zone.
	// 100 and 101 are 1% apart → same zone.
	lows := []float64{120, 110, 100, 120, 115, 110, 101, 120, 115}
	highs := []float64{130, 120, 110, 130, 125, 120, 111, 130, 125}

	r := analysis.FindZones(highs, lows, defaultOpts)
	for _, z := range r.Support {
		if z.Low <= 101 && z.High >= 100 && z.Touches >= 2 {
			return // found merged zone
		}
	}
	t.Errorf("expected lows 100 and 101 to merge into one zone, got %+v", r.Support)
}

func TestFindZones_ClusteredLowsDoNotMergeWhenFar(t *testing.T) {
	// 100 and 115 are 15% apart — must NOT merge with 2% threshold.
	lows := []float64{120, 110, 100, 120, 115, 110, 115, 120, 115}
	highs := []float64{130, 120, 110, 130, 125, 120, 125, 130, 125}

	r := analysis.FindZones(highs, lows, defaultOpts)
	for _, z := range r.Support {
		if z.Low <= 100 && z.High >= 115 {
			t.Errorf("100 and 115 should not be in the same zone: %+v", z)
		}
	}
}

// ---------------------------------------------------------------------------
// Resistance zones
// ---------------------------------------------------------------------------

func TestFindZones_SingleClearHigh(t *testing.T) {
	highs := []float64{50, 55, 58, 80, 57, 54, 50, 52, 49}
	lows := []float64{45, 50, 53, 75, 52, 49, 45, 47, 44}
	r := analysis.FindZones(highs, lows, defaultOpts)

	if len(r.Resistance) == 0 {
		t.Fatal("expected at least one resistance zone")
	}
	found := false
	for _, z := range r.Resistance {
		if z.Low <= 80 && z.High >= 80 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected resistance zone around 80, got %+v", r.Resistance)
	}
}

func TestFindZones_ClusteredHighsMerge(t *testing.T) {
	// Highs at 200 and 202 (1% apart) must merge.
	highs := []float64{180, 190, 200, 180, 185, 190, 202, 180, 185}
	lows := []float64{170, 180, 190, 170, 175, 180, 192, 170, 175}

	r := analysis.FindZones(highs, lows, defaultOpts)
	for _, z := range r.Resistance {
		if z.Low <= 200 && z.High >= 202 && z.Touches >= 2 {
			return
		}
	}
	t.Errorf("expected highs 200 and 202 to merge into one zone, got %+v", r.Resistance)
}

// ---------------------------------------------------------------------------
// Sorting (strongest zone first)
// ---------------------------------------------------------------------------

func TestFindZones_SortedByTouches(t *testing.T) {
	// Build a series with one level touched 3× and another touched 1×.
	// The 3-touch zone must appear first.
	lows := []float64{
		100, 90, 80, 100, 95, // low near 80 (touch 1)
		100, 90, 80, 100, 95, // low near 80 (touch 2)
		100, 90, 80, 100, 95, // low near 80 (touch 3)
		100, 90, 60, 100, 95, // low near 60 (touch 1 only)
	}
	highs := make([]float64, len(lows))
	for i, v := range lows {
		highs[i] = v + 10
	}

	r := analysis.FindZones(highs, lows, defaultOpts)
	if len(r.Support) < 2 {
		t.Skipf("need ≥2 support zones to test ordering, got %d", len(r.Support))
	}
	if r.Support[0].Touches < r.Support[1].Touches {
		t.Errorf("zones not sorted by touches: %+v", r.Support)
	}
}

// ---------------------------------------------------------------------------
// ZoneOptions defaults
// ---------------------------------------------------------------------------

func TestFindZones_ZeroOptsUsesDefaults(t *testing.T) {
	// Passing a zero ZoneOptions must not panic and must still find zones.
	lows := []float64{50, 45, 42, 30, 43, 46, 50, 48, 51}
	highs := []float64{55, 50, 47, 35, 48, 51, 55, 53, 56}
	r := analysis.FindZones(highs, lows, analysis.ZoneOptions{})
	if len(r.Support) == 0 {
		t.Error("expected at least one support zone with default options")
	}
}

// ---------------------------------------------------------------------------
// MinResistanceTouches filter
// ---------------------------------------------------------------------------

func TestFindZones_MinResistanceTouches_Filters1TouchZone(t *testing.T) {
	// Single spike high → 1-touch resistance. MinResistanceTouches=2 must drop it.
	highs := []float64{50, 55, 58, 80, 57, 54, 50, 52, 49}
	lows  := []float64{45, 50, 53, 75, 52, 49, 45, 47, 44}
	opts := analysis.ZoneOptions{Window: 2, ClusterPct: 0.02, MinResistanceTouches: 2}
	r := analysis.FindZones(highs, lows, opts)
	for _, z := range r.Resistance {
		if z.Touches < 2 {
			t.Errorf("resistance zone with %d touches survived MinResistanceTouches=2: %+v", z.Touches, z)
		}
	}
}

func TestFindZones_MinResistanceTouches_Passes2TouchZone(t *testing.T) {
	// Two spike highs near 200 → merged 2-touch zone must survive min=2.
	highs := []float64{180, 190, 200, 180, 185, 190, 202, 180, 185}
	lows  := []float64{170, 180, 190, 170, 175, 180, 192, 170, 175}
	opts := analysis.ZoneOptions{Window: 2, ClusterPct: 0.02, MinResistanceTouches: 2}
	r := analysis.FindZones(highs, lows, opts)
	found := false
	for _, z := range r.Resistance {
		if z.Low <= 202 && z.High >= 200 && z.Touches >= 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 2-touch zone around 200–202 to survive min=2 filter, got %+v", r.Resistance)
	}
}

func TestFindZones_MinResistanceTouches_DisabledWhenZeroOrOne(t *testing.T) {
	// MinResistanceTouches=0 and =1 must both allow 1-touch zones through.
	highs := []float64{50, 55, 58, 80, 57, 54, 50, 52, 49}
	lows  := []float64{45, 50, 53, 75, 52, 49, 45, 47, 44}
	for _, min := range []int{0, 1} {
		opts := analysis.ZoneOptions{Window: 2, ClusterPct: 0.02, MinResistanceTouches: min}
		r := analysis.FindZones(highs, lows, opts)
		if len(r.Resistance) == 0 {
			t.Errorf("MinResistanceTouches=%d should allow 1-touch zones but got no resistance zones", min)
		}
	}
}

func TestFindZones_ZoneMidIsAverage(t *testing.T) {
	lows := []float64{50, 45, 42, 30, 43, 46, 50, 48, 51}
	highs := []float64{55, 50, 47, 35, 48, 51, 55, 53, 56}
	r := analysis.FindZones(highs, lows, defaultOpts)

	for _, z := range append(r.Support, r.Resistance...) {
		want := (z.Low + z.High) / 2
		if !approxEqual(z.Mid, want) {
			t.Errorf("zone Mid = %.4f, want %.4f for zone %+v", z.Mid, want, z)
		}
	}
}
