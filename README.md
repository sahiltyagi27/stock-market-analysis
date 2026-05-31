# Stock Market Analysis Engine

[![CI](https://github.com/sahiltyagi27/stock-market-analysis/actions/workflows/ci.yml/badge.svg)](https://github.com/sahiltyagi27/stock-market-analysis/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/go-1.26-00ADD8?logo=go)](https://go.dev)

A backend engine written in Go that automates stock market technical analysis — from raw OHLCV candle data to ranked, explainable trade opportunities.

---

## Overview

Retail investors cannot manually scan hundreds of stocks every day.

This engine solves that by running a complete analysis pipeline automatically:

1. Loads historical OHLCV candles (CSV or database)
2. Calculates exponential moving averages to determine trend
3. Detects support and resistance zones from price structure
4. Generates trade setups with entry, stop-loss, and target
5. Scores and ranks opportunities across a universe of symbols
6. Returns explainable reasons for every signal generated

---

## Features

- ✅ **EMA Engine** — 10, 50, and 200-period exponential moving averages
- ✅ **Support Detection** — local minima clustering into price zones
- ✅ **Resistance Detection** — local maxima clustering into price zones
- ✅ **Trade Analyzer** — entry, stop-loss, target, risk, reward calculation
- ✅ **Risk/Reward Grading** — Poor / Fair / Good / Excellent quality grades
- ✅ **Scanner Engine** — bullish filter, multi-stock ranking by score
- ✅ **Volume Confirmation** — rolling average comparison, spike detection
- ✅ **Explainable Signals** — human-readable reasons for every signal

---

## Architecture

```
Historical Candles (OHLCV)
          │
          ▼
     EMA Engine
  (EMA 10 / 50 / 200)
          │
          ▼
   Zone Detection
(Support & Resistance)
          │
          ▼
   Trade Analyzer
(Entry · SL · Target · R/R)
          │
          ▼
  Scanner & Scoring
 (Trend + RR + Support + Volume)
          │
          ▼
   Ranked Signals
  with Reasons [ ]
```

---

## Example Signal

```json
{
  "symbol": "APOLLOTYRE",
  "price": 412.50,
  "trend": "bullish",
  "score": 84.5,
  "ema": {
    "ema_10": 408.30,
    "ema_50": 389.75,
    "ema_200": 351.20
  },
  "support": {
    "low": 388.00,
    "high": 391.50,
    "mid": 389.75,
    "touches": 3
  },
  "resistance": {
    "low": 448.00,
    "high": 453.00,
    "mid": 450.50,
    "touches": 2
  },
  "trade": {
    "direction": "long",
    "entry": 412.50,
    "stop_loss": 386.06,
    "target": 450.50,
    "risk": 26.44,
    "reward": 38.00,
    "risk_reward": 2.85,
    "quality": "good"
  },
  "reasons": [
    "Price above EMA50 (389.75) and EMA200 (351.20)",
    "Risk/Reward 2.85 exceeds minimum 2.00",
    "Support zone touched 3 times",
    "Trade quality: good",
    "Volume 1.4x above rolling average"
  ]
}
```

---

## Project Structure

```
stock-market-analysis/
├── cmd/server/          # HTTP server entry point
├── config/              # Environment config loader
├── internal/
│   ├── analysis/        # EMA, zone detection, trade analyzer
│   ├── api/             # REST handlers (Chi router)
│   ├── loader/          # CSV → Candle parser
│   ├── scanner/         # Scanner engine, scorer, signal reasons
│   ├── service/         # Application service layer
│   └── store/           # PostgreSQL candle store
├── pkg/models/          # Shared domain types (Candle)
└── data/                # Sample OHLCV CSV files
```

---

## Getting Started

### Prerequisites

- Go 1.26+
- PostgreSQL

### Setup

```bash
# Clone the repo
git clone https://github.com/sahiltyagi27/stock-market-analysis.git
cd stock-market-analysis

# Copy and fill environment variables
cp .env.example .env

# Load sample data and start the server
go run ./cmd/server -load data/AAPL_sample.csv -symbol AAPL

# Or just start the server (if data is already loaded)
go run ./cmd/server
```

### API Endpoints

| Method | Endpoint | Description |
|---|---|---|
| GET | `/stocks/{symbol}/candles` | Full candle history (supports `?from`, `?to`, `?limit`) |
| GET | `/stocks/{symbol}/latest` | Most recent candle |

### Environment Variables

| Variable | Default | Description |
|---|---|---|
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5432` | PostgreSQL port |
| `DB_USER` | `postgres` | Database user |
| `DB_PASSWORD` | _(required)_ | Database password |
| `DB_NAME` | `stocks` | Database name |
| `SERVER_PORT` | `8080` | HTTP listen port |

---

## Running Tests

```bash
go test ./...
```

```
ok  github.com/sahiltyagi27/stock-market-analysis/internal/analysis
ok  github.com/sahiltyagi27/stock-market-analysis/internal/scanner
```

---

## Roadmap

**Completed**
- [x] M1 — Historical data foundation (CSV loader, PostgreSQL, REST API)
- [x] M2 — EMA engine (10 / 50 / 200-period)
- [x] M3 — Support & resistance zone detection
- [x] M4 — Trade analyzer (SL, target, R/R grading)
- [x] M5 — Scanner engine (bullish filter, scoring, explainable signals)

**Upcoming**
- [ ] M6 — Backtesting engine (walk-forward simulation, win rate, profit factor)
- [ ] M7 — Concurrent worker pool (parallel scanning across 500+ stocks)
- [ ] M8 — Daily scan scheduler + signals API
- [ ] M9 — Kafka pipeline (market data producer → scanner consumers)
- [ ] M10 — Production architecture (Docker, CI/CD, ClickHouse)

---

## Design Decisions

**EMA trend filtering**
The scanner only emits signals where price is above both EMA50 and EMA200. This ensures only stocks in confirmed uptrends are considered for long setups, filtering out noise from sideways and bear markets.

**Support / resistance clustering**
Local extrema (price lows and highs) are detected using a ±window neighbourhood check, then merged into zones using a greedy price-distance threshold (default 2%). This avoids treating micro-variations of the same level as separate zones, and the touch count gives a natural strength ranking.

**Risk/reward evaluation**
Stop-loss is placed just below the support zone low (0.5% buffer) and the target is the resistance zone mid-point. This mirrors how discretionary traders set up swing trades and gives a deterministic, reproducible R/R calculation.

**Explainable signals**
Every `StockSignal` carries a `Reasons []string` field built from the same inputs used for scoring. This makes the scanner auditable — every number in the score maps to a human-readable sentence — and sets the groundwork for a UI that shows users why each stock was selected.
