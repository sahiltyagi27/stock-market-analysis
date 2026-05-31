package analysis

import (
	"errors"
	"math"
)

// Direction of a trade setup.
type Direction string

const (
	DirectionLong  Direction = "long"
	DirectionShort Direction = "short"
)

// Quality grades a trade by its risk/reward ratio.
type Quality string

const (
	QualityPoor      Quality = "poor"      // R/R < 1.5
	QualityFair      Quality = "fair"      // 1.5 ≤ R/R < 2.0
	QualityGood      Quality = "good"      // 2.0 ≤ R/R < 3.0
	QualityExcellent Quality = "excellent" // R/R ≥ 3.0
)

// TradeSetup is the full output of the analyzer for one trade direction.
type TradeSetup struct {
	Direction  Direction `json:"direction"`
	Entry      float64   `json:"entry"`
	StopLoss   float64   `json:"stop_loss"`
	Target     float64   `json:"target"`
	Risk       float64   `json:"risk"`        // |Entry - StopLoss|
	Reward     float64   `json:"reward"`      // |Target - Entry|
	RiskReward float64   `json:"risk_reward"` // Reward / Risk
	Quality    Quality   `json:"quality"`
}

// TradeAnalysis holds setups for both directions so the caller can pick
// whichever fits their bias, or compare both.
type TradeAnalysis struct {
	Long  *TradeSetup `json:"long,omitempty"`
	Short *TradeSetup `json:"short,omitempty"`
}

// AnalyzerOptions controls how the trade analyzer places stops and targets.
type AnalyzerOptions struct {
	// SLBufferPct is the distance beyond a zone boundary where the stop loss
	// is placed, as a fraction of price. Default: 0.005 (0.5%).
	SLBufferPct float64
}

func (o *AnalyzerOptions) withDefaults() AnalyzerOptions {
	out := *o
	if out.SLBufferPct <= 0 {
		out.SLBufferPct = 0.005
	}
	return out
}

var (
	ErrInvalidPrice    = errors.New("current price must be > 0")
	ErrZonesOverlap    = errors.New("support and resistance zones overlap")
	ErrPriceOutOfRange = errors.New("current price is outside the support-resistance range")
	ErrZeroRisk        = errors.New("stop loss equals entry: risk is zero")
	ErrZeroReward      = errors.New("target equals entry: reward is zero")
)

// Analyze computes long and short trade setups from the current price and the
// nearest support and resistance zones.
//
// Long setup  — entry at current price, SL below support.Low, target at resistance.Mid.
// Short setup — entry at current price, SL above resistance.High, target at support.Mid.
//
// Both setups are always returned when the price is within the range; the
// caller uses Direction to pick the one that matches their market bias.
func Analyze(price float64, support, resistance Zone, opts AnalyzerOptions) (TradeAnalysis, error) {
	o := opts.withDefaults()

	if price <= 0 {
		return TradeAnalysis{}, ErrInvalidPrice
	}
	if support.Mid >= resistance.Mid || support.High >= resistance.Low {
		return TradeAnalysis{}, ErrZonesOverlap
	}
	if price <= support.Low || price >= resistance.High {
		return TradeAnalysis{}, ErrPriceOutOfRange
	}

	long, err := buildLong(price, support, resistance, o.SLBufferPct)
	if err != nil {
		return TradeAnalysis{}, err
	}
	short, err := buildShort(price, support, resistance, o.SLBufferPct)
	if err != nil {
		return TradeAnalysis{}, err
	}

	return TradeAnalysis{Long: &long, Short: &short}, nil
}

func buildLong(price float64, support, resistance Zone, slBuf float64) (TradeSetup, error) {
	entry := round2(price)
	sl := round2(support.Low * (1 - slBuf))
	target := round2(resistance.Mid)

	risk := round2(entry - sl)
	reward := round2(target - entry)

	if risk <= 0 {
		return TradeSetup{}, ErrZeroRisk
	}
	if reward <= 0 {
		return TradeSetup{}, ErrZeroReward
	}

	rr := round2(reward / risk)
	return TradeSetup{
		Direction:  DirectionLong,
		Entry:      entry,
		StopLoss:   sl,
		Target:     target,
		Risk:       risk,
		Reward:     reward,
		RiskReward: rr,
		Quality:    GradeRR(rr),
	}, nil
}

func buildShort(price float64, support, resistance Zone, slBuf float64) (TradeSetup, error) {
	entry := round2(price)
	sl := round2(resistance.High * (1 + slBuf))
	target := round2(support.Mid)

	risk := round2(sl - entry)
	reward := round2(entry - target)

	if risk <= 0 {
		return TradeSetup{}, ErrZeroRisk
	}
	if reward <= 0 {
		return TradeSetup{}, ErrZeroReward
	}

	rr := round2(reward / risk)
	return TradeSetup{
		Direction:  DirectionShort,
		Entry:      entry,
		StopLoss:   sl,
		Target:     target,
		Risk:       risk,
		Reward:     reward,
		RiskReward: rr,
		Quality:    GradeRR(rr),
	}, nil
}

// GradeRR maps a risk/reward ratio to a Quality label.
func GradeRR(rr float64) Quality {
	switch {
	case rr >= 3.0:
		return QualityExcellent
	case rr >= 2.0:
		return QualityGood
	case rr >= 1.5:
		return QualityFair
	default:
		return QualityPoor
	}
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
