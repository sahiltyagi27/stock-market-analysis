package analysis

import "sort"

// Zone represents a price cluster that acts as support or resistance.
type Zone struct {
	Low     float64 // lowest price in the cluster
	High    float64 // highest price in the cluster
	Mid     float64 // arithmetic centre of the zone
	Touches int     // number of local extremes that formed this zone
}

// ZoneResult holds the detected support and resistance zones,
// sorted strongest-first (most touches → tightest spread).
type ZoneResult struct {
	Support    []Zone `json:"support"`
	Resistance []Zone `json:"resistance"`
}

// ZoneOptions controls zone detection sensitivity.
type ZoneOptions struct {
	// Window is the number of candles on each side used to confirm a local
	// extreme. A candle at index i is a local low when its Low is the lowest
	// in [i-Window, i+Window]. Default: 2.
	Window int

	// ClusterPct is the maximum price distance (as a fraction of price) within
	// which two extremes are merged into the same zone. Default: 0.02 (2 %).
	ClusterPct float64
}

func (o *ZoneOptions) withDefaults() ZoneOptions {
	out := *o
	if out.Window <= 0 {
		out.Window = 2
	}
	if out.ClusterPct <= 0 {
		out.ClusterPct = 0.02
	}
	return out
}

// FindZones detects support and resistance zones from OHLC candle data.
//
// Algorithm:
//  1. Scan for local lows  (Low  is the lowest  in a ±Window neighbourhood).
//  2. Scan for local highs (High is the highest in a ±Window neighbourhood).
//  3. Cluster each set of extremes: greedily merge any two prices whose
//     relative distance ≤ ClusterPct into a single zone.
//  4. Return zones sorted by strength (touches desc, then width asc).
func FindZones(highs, lows []float64, opts ZoneOptions) ZoneResult {
	o := opts.withDefaults()

	localLows := localMinima(lows, o.Window)
	localHighs := localMaxima(highs, o.Window)

	return ZoneResult{
		Support:    clusterToZones(localLows, o.ClusterPct),
		Resistance: clusterToZones(localHighs, o.ClusterPct),
	}
}

// localMinima returns the Low values that are true local minima within ±window.
func localMinima(lows []float64, window int) []float64 {
	var out []float64
	n := len(lows)
	for i := window; i < n-window; i++ {
		if isLocalMin(lows, i, window) {
			out = append(out, lows[i])
		}
	}
	return out
}

// localMaxima returns the High values that are true local maxima within ±window.
func localMaxima(highs []float64, window int) []float64 {
	var out []float64
	n := len(highs)
	for i := window; i < n-window; i++ {
		if isLocalMax(highs, i, window) {
			out = append(out, highs[i])
		}
	}
	return out
}

func isLocalMin(prices []float64, i, window int) bool {
	for j := i - window; j <= i+window; j++ {
		if j != i && prices[j] <= prices[i] {
			return false
		}
	}
	return true
}

func isLocalMax(prices []float64, i, window int) bool {
	for j := i - window; j <= i+window; j++ {
		if j != i && prices[j] >= prices[i] {
			return false
		}
	}
	return true
}

// clusterToZones groups sorted price levels into zones using a greedy
// single-linkage approach: a price joins the current cluster when its
// relative distance to the cluster centre ≤ clusterPct.
func clusterToZones(prices []float64, clusterPct float64) []Zone {
	if len(prices) == 0 {
		return nil
	}

	sorted := make([]float64, len(prices))
	copy(sorted, prices)
	sort.Float64s(sorted)

	var zones []Zone
	clusterLow := sorted[0]
	clusterHigh := sorted[0]
	touches := 1

	flush := func() {
		mid := (clusterLow + clusterHigh) / 2
		zones = append(zones, Zone{
			Low:     clusterLow,
			High:    clusterHigh,
			Mid:     mid,
			Touches: touches,
		})
	}

	for _, p := range sorted[1:] {
		mid := (clusterLow + clusterHigh) / 2
		if mid > 0 && (p-mid)/mid <= clusterPct {
			// Extend current cluster.
			if p > clusterHigh {
				clusterHigh = p
			}
			touches++
		} else {
			flush()
			clusterLow = p
			clusterHigh = p
			touches = 1
		}
	}
	flush()

	sortZones(zones)
	return zones
}

// sortZones orders zones by strength: most touches first, then tightest spread.
func sortZones(zones []Zone) {
	sort.Slice(zones, func(i, j int) bool {
		if zones[i].Touches != zones[j].Touches {
			return zones[i].Touches > zones[j].Touches
		}
		wi := zones[i].High - zones[i].Low
		wj := zones[j].High - zones[j].Low
		return wi < wj
	})
}
