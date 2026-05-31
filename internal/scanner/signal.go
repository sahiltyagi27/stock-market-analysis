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
	Symbol     string            `json:"symbol"`
	Price      float64           `json:"price"`
	Trend      Trend             `json:"trend"`
	EMA        analysis.EMAResult `json:"ema"`
	Support    analysis.Zone     `json:"support"`
	Resistance analysis.Zone     `json:"resistance"`
	Trade      analysis.TradeSetup `json:"trade"` // long setup
	Score      float64           `json:"score"`
}

// Input is one stock's data fed into the scanner.
type Input struct {
	Symbol  string
	Candles []models.Candle
}
