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
	Reasons    []string            `json:"reasons"` // human-readable explanation of why this signal was selected
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
