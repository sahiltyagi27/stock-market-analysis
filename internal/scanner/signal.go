package scanner

import (
	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// Trend represents the EMA-based directional bias.
type Trend string

const (
	TrendBullish Trend = "bullish" // price > EMA50 and price > EMA200
	TrendBearish Trend = "bearish" // price < EMA50 and price < EMA200
	TrendNeutral Trend = "neutral" // mixed
)

// StockSignal is the full output for a single symbol after the scan pipeline.
type StockSignal struct {
	Symbol     string              `json:"symbol"`
	Price      float64             `json:"price"`
	Trend      Trend               `json:"trend"`
	EMA        analysis.EMAResult  `json:"ema"`
	Support    analysis.Zone       `json:"support"`
	Resistance analysis.Zone       `json:"resistance"`
	Trade      analysis.TradeSetup `json:"trade"` // long setup
	Score      float64             `json:"score"`
	Breakdown  ScoreBreakdown      `json:"breakdown"`
	Extension  Extension           `json:"extension"`
	Reasons    []string            `json:"reasons"` // human-readable explanation of why this signal was selected
}

// BreakoutSignal is a watchlist candidate sitting below a tested resistance
// zone. It is a "watch for confirmation" setup, not an immediate trade signal.
type BreakoutSignal struct {
	Symbol                  string             `json:"symbol"`
	Price                   float64            `json:"price"`
	Trend                   Trend              `json:"trend"`
	EMA                     analysis.EMAResult `json:"ema"`
	Support                 analysis.Zone      `json:"support"`
	Resistance              analysis.Zone      `json:"resistance"`
	Score                   float64            `json:"score"`
	DistanceToResistancePct float64            `json:"distance_to_resistance_pct"`
	BreakoutPrice           float64            `json:"breakout_price"`
	Extension               Extension          `json:"extension"`
	Volume                  VolumeConfirmation `json:"volume"`
	Reasons                 []string           `json:"reasons"`
}

// VolumeConfirmation explains whether current volume is confirming interest.
type VolumeConfirmation struct {
	AvgVolume   float64 `json:"avg_volume"`
	LastVolume  float64 `json:"last_volume"`
	VolumeRatio float64 `json:"volume_ratio"`
}

// ScoreBreakdown explains how StockSignal.Score was composed.
type ScoreBreakdown struct {
	Trend       float64 `json:"trend"`
	RR          float64 `json:"rr"`
	Support     float64 `json:"support"`
	Volume      float64 `json:"volume"`
	AvgVolume   float64 `json:"avg_volume"`
	LastVolume  float64 `json:"last_volume"`
	VolumeRatio float64 `json:"volume_ratio"`
}

// Extension shows whether a setup may be late after a recent rally.
// Values are percentages, where positive means price is above the reference.
type Extension struct {
	FromEMA10Pct       float64 `json:"from_ema_10_pct"`
	FromEMA50Pct       float64 `json:"from_ema_50_pct"`
	FromSupportHighPct float64 `json:"from_support_high_pct"`
	Move10DPct         float64 `json:"move_10d_pct"`
	HasMove10D         bool    `json:"has_move_10d"`
}

// Diagnostic explains how far one stock got through the scanner pipeline,
// including symbols that were filtered before becoming signals.
type Diagnostic struct {
	Symbol string             `json:"symbol"`
	Price  float64            `json:"price"`
	Trend  Trend              `json:"trend"`
	EMA    analysis.EMAResult `json:"ema"`
	Error  string             `json:"error,omitempty"`
}

// Input is one stock's data fed into the scanner.
type Input struct {
	Symbol  string
	Candles []models.Candle
}
