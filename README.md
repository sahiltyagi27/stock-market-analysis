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
- ✅ **Kite Connect** — token exchange, instrument lookup, historical data sync
- ✅ **Score Breakdown** — per-component score transparency (trend / R/R / support / volume)
- ✅ **Live Scanner** — Kite WebSocket (full mode), runs every 2 min during market hours, merges live ticks with DB history
- ✅ **NSE Holiday Calendar** — automatically skips all NSE trading holidays (no false "no signals" on holidays)
- ✅ **Signal Persistence** — new signals marked `[NEW]`; consecutive appearances show a streak counter `×N`
- ✅ **Liquidity Filter** — optional minimum avg daily volume threshold to exclude illiquid stocks
- ✅ **Relative Strength vs NIFTY 50** — swing signals can require 20D outperformance; live output also shows intraday RS from open
- ✅ **Persistent Signal Log** — every scan run is written to `scan_results` (PostgreSQL) for post-hoc review and backtesting

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
    "low": 485.00,
    "high": 490.70,
    "mid": 487.85,
    "touches": 2
  },
  "trade": {
    "direction": "long",
    "entry": 412.50,
    "stop_loss": 386.06,
    "target": 487.85,
    "risk": 26.44,
    "reward": 75.35,
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
├── cmd/
│   ├── kite-sync/       # Download daily candles from Kite → PostgreSQL
│   ├── kite-token/      # Exchange Kite request_token for access_token
│   ├── live-scan/       # Real-time scanner via Kite WebSocket (every 2 min)
│   ├── scan/            # Offline scanner (CSV / CSV dir / DB modes)
│   └── server/          # REST API server
├── config/              # Environment config loader + symbols watchlist
├── internal/
│   ├── analysis/        # EMA, zone detection, trade analyzer
│   ├── api/             # REST handlers (Chi router)
│   ├── kite/            # Kite Connect client (token, instruments, history)
│   ├── loader/          # CSV → Candle parser
│   ├── scanner/         # Scanner engine, scorer, signal reasons, diagnostics
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
```

### Command Cookbook

#### Start PostgreSQL

The scanner stores Kite daily candles in PostgreSQL. Start one local database
before running `kite-sync`, `scan --db`, `live-scan`, or the HTTP server.

If port `5432` is free:

```bash
docker run --name stock-market-analysis-postgres \
  -e POSTGRES_USER=postgres \
  -e POSTGRES_PASSWORD=secret \
  -e POSTGRES_DB=stocks \
  -p 5432:5432 \
  -d postgres:16-alpine
```

If another Postgres is already using `5432`, use `5433`:

```bash
docker run --name stock-market-analysis-postgres \
  -e POSTGRES_USER=postgres \
  -e POSTGRES_PASSWORD=secret \
  -e POSTGRES_DB=stocks \
  -p 5433:5432 \
  -d postgres:16-alpine
```

Then set `.env` to match:

```env
DB_HOST=localhost
DB_PORT=5433
DB_USER=postgres
DB_PASSWORD=secret
DB_NAME=stocks
SERVER_PORT=8080
```

Useful Docker commands:

```bash
docker ps
docker start stock-market-analysis-postgres
docker stop stock-market-analysis-postgres
docker logs stock-market-analysis-postgres
```

#### Configure Kite

Kite is used for instrument lookup, historical daily candles, and live ticks.
Create a Kite Connect app, then put the app credentials in `.env`:

```env
KITE_API_KEY=your_api_key
KITE_API_SECRET=your_api_secret
KITE_ACCESS_TOKEN=
KITE_BASE_URL=https://api.kite.trade
```

Use this redirect URL in the Kite developer console:

```text
http://127.0.0.1:8080/kite/callback
```

#### Refresh Kite Access Token

Kite access tokens expire daily. Run this command first to print the Kite login
URL:

```bash
go run ./cmd/kite-token
```

Open the printed login URL, complete Kite login, copy `request_token` from the
redirect URL, then exchange it:

```bash
go run ./cmd/kite-token --request-token <request_token_from_redirect>
```

Copy the printed value into `.env`:

```env
KITE_ACCESS_TOKEN=generated_access_token
```

#### Sync Kite Daily Candles

This downloads historical daily OHLCV candles from Kite and stores them in
PostgreSQL. Run this after setting `KITE_ACCESS_TOKEN`, and refresh it whenever
you want the DB cache to include the latest completed daily candle.

```bash
go run ./cmd/kite-sync --symbols config/symbols.txt --period 2y
```

What it does:
- reads symbols from `config/symbols.txt`
- finds each NSE instrument in Kite's instrument master
- downloads the requested historical period
- upserts candles into the `candles` table
- also syncs NIFTY 50 index candles as `NIFTY50` by default for relative-strength filters
- also syncs verified NSE sector index candles by default for future sector-strength filters

Common variants:

For another exchange:

```bash
go run ./cmd/kite-sync --exchange BSE --symbols config/symbols.txt --period 2y
```

Sync only a smaller temporary watchlist:

```bash
printf "EXIDEIND\nITC\n" > /tmp/my-symbols.txt
go run ./cmd/kite-sync --symbols /tmp/my-symbols.txt --period 2y
```

Skip NIFTY benchmark sync:

```bash
go run ./cmd/kite-sync --symbols config/symbols.txt --period 2y --include-nifty=false
```

Skip sector index sync:

```bash
go run ./cmd/kite-sync --symbols config/symbols.txt --period 2y --include-sector-indices=false
```

Sync only selected sector indices:

```bash
go run ./cmd/kite-sync --symbols config/symbols.txt --period 2y --sector-indices "NIFTY BANK,NIFTY IT,NIFTY PHARMA"
```

Sector index candles are stored using compact DB symbols such as `NIFTYBANK`,
`NIFTYIT`, `NIFTYPHARMA`, `NIFTYMETAL`, and `NIFTYFINSERVICE`.

#### Discover Sector Index Support

Before adding sector-strength filters, check which NSE sector indices Kite
exposes and whether their daily historical candles can be fetched.

```bash
go run ./cmd/sector-index-discovery
```

What it does:
- downloads Kite's NSE instrument master
- looks for known NSE sector index names such as `NIFTY BANK`, `NIFTY IT`,
  `NIFTY AUTO`, `NIFTY PHARMA`, `NIFTY FMCG`, and others
- probes daily historical candles over the requested period
- prints token, type, segment, candle count, latest candle date, and status
- does not write to PostgreSQL

Common variants:

```bash
go run ./cmd/sector-index-discovery --period 30d
go run ./cmd/sector-index-discovery --indices "NIFTY BANK,NIFTY IT,NIFTY PHARMA"
```

#### Scan Synced DB Candles

This runs the offline scanner against candles already stored in PostgreSQL.
It does not call Kite. Use this for end-of-day scans, debugging one stock, or
checking why a symbol is filtered out.

Full watchlist scan:

```bash
go run ./cmd/scan --db --symbols config/symbols.txt --top 10
```

Breakout watchlist scan:

```bash
go run ./cmd/scan --db --symbols config/symbols.txt --mode breakout --top 10
```

Show both swing entries and breakout watch candidates:

```bash
go run ./cmd/scan --db --symbols config/symbols.txt --mode all --top 10
```

What it shows:
- only valid swing trade candidates by default
- breakout watch candidates when `--mode breakout` or `--mode all` is used
- score breakdown: trend, R/R, support, volume
- historical relative strength vs `NIFTY50` for swing signals when benchmark candles are available
- price, trend, entry, stop-loss, target
- support/resistance zones and reasons
- final counts for scanned, skipped, and signal symbols

Single symbol scan:

```bash
go run ./cmd/scan --db --symbol RELIANCE --top 1
```

Single symbol with rejection details:

```bash
go run ./cmd/scan --db --symbol EXIDEIND --top 1 --show-filtered
```

Use `--show-filtered` when there is no signal and you want to see the price,
EMA10/50/200, trend, and the exact rejection reason. Reasons can include
bearish/neutral trend, price too close to EMA200, no valid support/resistance
zone, R/R below minimum, too few resistance touches, low average volume, or a
setup that is already extended after a recent rally. With the default
relative-strength filter, a stock can also be rejected when it has not
outperformed `NIFTY50` over the last 20 candles.

Useful stricter/looser filters:

```bash
go run ./cmd/scan --db --symbols config/symbols.txt --top 10 --min-rr 3
go run ./cmd/scan --db --symbols config/symbols.txt --top 10 --ema-margin 0
go run ./cmd/scan --db --symbols config/symbols.txt --top 10 --min-volume 200000
go run ./cmd/scan --db --symbols config/symbols.txt --top 10 --min-resistance-touches 1
go run ./cmd/scan --db --symbols config/symbols.txt --top 10 --max-10d-move 15
go run ./cmd/scan --db --symbols config/symbols.txt --top 10 --max-ema50-extension -1
go run ./cmd/scan --db --symbols config/symbols.txt --top 10 --rs-lookback 50
go run ./cmd/scan --db --symbols config/symbols.txt --top 10 --rs-lookback 0
go run ./cmd/scan --db --symbols config/symbols.txt --mode breakout --max-breakout-distance 2
```

Flag notes:
- `--mode`: `swing`, `breakout`, or `all`; default is `swing`
- `--min-rr`: minimum risk/reward required before a signal is printed
- `--ema-margin`: minimum percent price must be above EMA200; `0` disables it
- `--min-volume`: minimum previous-20-day average volume; `0` disables it
- `--min-resistance-touches`: default `2` avoids one-day spike resistance zones
- `--max-ema10-extension`: default `8`; filters stocks already too far above EMA10
- `--max-ema50-extension`: default `15`; filters stocks already too far above EMA50
- `--max-support-extension`: default `5`; filters entries too far above support
- `--max-10d-move`: default `12`; filters stocks that already rallied too much in 10 candles
- `--max-breakout-distance`: default `3`; max percent below resistance for breakout watch
- `--rs-lookback`: default `20`; swing stocks must outperform the benchmark over this many candles; `0` disables
- `--min-rs-pct`: default `0`; minimum outperformance vs benchmark over `--rs-lookback`
- `--rs-symbol`: default `NIFTY50`; benchmark symbol loaded from PostgreSQL or a matching CSV file
- set any `--max-*` extension flag below `0` to disable that specific guard

#### Live Scan (Real-Time via Kite WebSocket)

Connects to the Kite WebSocket feed, subscribes all watchlist symbols in **full mode**, and runs the scanner every 2 minutes during NSE market hours (09:15–15:30 IST, Mon–Fri).

Each run merges the live tick (LTP, Open, High, Low, Volume) as today's candle on top of 2 years of historical candles from PostgreSQL, then runs the full scanner pipeline.

```bash
go run ./cmd/live-scan
```

With options:

```bash
go run ./cmd/live-scan --top 10 --interval 2m --min-rr 2.0
go run ./cmd/live-scan --mode breakout --top 10
go run ./cmd/live-scan --mode all --top 10
go run ./cmd/live-scan --interval 30s            # faster cadence
go run ./cmd/live-scan --dev                     # disable market hours check (for testing)
```

Live scan for one stock:

```bash
printf "EXIDEIND\n" > /tmp/exideind-symbols.txt
go run ./cmd/live-scan --symbols /tmp/exideind-symbols.txt --top 1
```

Testing one stock outside market hours:

```bash
go run ./cmd/live-scan --symbols /tmp/exideind-symbols.txt --top 1 --interval 30s --dev
```

What live scan does:
- subscribes to Kite WebSocket ticks in full mode
- keeps today's candle updated from live LTP/open/high/low/volume
- merges that live candle with historical DB candles
- runs swing and/or breakout scanner modes repeatedly
- filters swing signals by 20D relative strength vs `NIFTY50` when benchmark candles exist
- shows `[NEW]` for fresh signals and `xN`/`×N` streaks for repeated signals
- writes emitted swing signals to `scan_results`

Available flags:

| Flag | Default | Description |
|---|---|---|
| `--symbols` | `config/symbols.txt` | Watchlist file |
| `--top` | `10` | Signals to print per run |
| `--mode` | `swing` | Scanner mode: `swing`, `breakout`, or `all` |
| `--min-rr` | `2.0` | Minimum risk/reward ratio |
| `--interval` | `2m` | Scan interval (e.g. `30s`, `2m`, `5m`) |
| `--ema-margin` | `1.0` | Minimum % gap required between price and EMA200; `0` disables |
| `--min-volume` | `0` | Minimum 20-day avg daily volume; `0` disables (e.g. `200000`) |
| `--min-resistance-touches` | `2` | Minimum touches for a resistance zone to qualify; `1` allows all |
| `--max-ema10-extension` | `8.0` | Maximum % above EMA10 before filtering as extended; `<0` disables |
| `--max-ema50-extension` | `15.0` | Maximum % above EMA50 before filtering as extended; `<0` disables |
| `--max-support-extension` | `5.0` | Maximum % above support high before filtering as extended; `<0` disables |
| `--max-10d-move` | `12.0` | Maximum 10-candle % move before filtering as extended; `<0` disables |
| `--max-breakout-distance` | `3.0` | Maximum % below resistance for breakout watch candidates; `<0` disables |
| `--rs-lookback` | `20` | Swing relative-strength lookback vs benchmark; `0` disables |
| `--min-rs-pct` | `0` | Minimum stock outperformance vs benchmark over `--rs-lookback` |
| `--rs-symbol` | `NIFTY50` | Benchmark DB symbol for relative-strength filter |
| `--period` | `2y` | Historical candle window for EMA/zone computation |
| `--exchange` | `NSE` | Kite exchange |
| `--dev` | `false` | Disable market hours check |

Example output:

```
━━━  Live Scan  02-Jun-2026  10:15:00 IST  ━━━

  1. HDFCBANK        ₹1625.50    Score: 87/100  ×3
     ├ Trend:   40/40  R/R: 22/30  Support: 20/20  Volume: 5/10 (est. 3500000 vs avg 2100000 = 1.67x)
     ├ RS vs NIFTY: +1.23%  (NIFTY: +0.47%)
     ├ Trend: bullish   R/R: 2.85 (good)
     ├ Entry: 1625.50   SL: 1580.20   Target: 1750.00
     ├ Support:    1580.00–1590.00 (3 touches)
     ├ Resistance: 1745.00–1760.00 (2 touches)
     └ Reasons:
         • Price above EMA50 (1520.00) and EMA200 (1380.00)
         • Risk/Reward 2.85 exceeds minimum 2.00
         • Support zone touched 3 times
         • Trade quality: good
         • Volume 1.7x above rolling average

  2. TATASTEEL       ₹142.30     Score: 74/100  [NEW]
     ├ Trend:   40/40  R/R: 18/30  Support: 15/20  Volume: 1/10 (est. 8200000 vs avg 9100000 = 0.90x)
     ├ RS vs NIFTY: +0.45%  (NIFTY: +0.47%)
     ...

  ──────────────────────────────────────────────────────
  Scanned: 487   Signals: 6    No tick yet: 8
  * volume projected to full-day (48% of session elapsed)
  NIFTY 50: +0.47% from open
```

**Signal tags:**
- `×N` — signal has appeared in N consecutive scans (e.g. `×3` = present for 6 minutes straight)
- `[NEW]` — appeared in this scan but was absent from the previous one

> **Prerequisites:** `KITE_API_KEY` and `KITE_ACCESS_TOKEN` must be set. Refresh the access token daily with `cmd/kite-token`. Populate the DB with `cmd/kite-sync` before the first run.

#### Scan Manual CSV Files

CSV mode is useful when you want to test one downloaded file without Kite or
PostgreSQL. The CSV should contain OHLCV data; Google Finance exports are
supported by the loader.

Single Google Finance CSV:

```bash
go run ./cmd/scan --csv ~/Desktop/ITC.csv --symbol ITC --top 3
```

What it does:
- reads only the CSV file you pass
- uses `--symbol` as the stock name in output
- runs the same EMA, zone, trade, and scoring pipeline
- does not write to PostgreSQL

Folder of CSVs named by symbol:

```bash
go run ./cmd/scan --csv-dir ~/Desktop/nifty-data --top 10
```

This scans every `.csv` file in the folder and derives each symbol from the
filename, for example `~/Desktop/nifty-data/ITC.csv` becomes `ITC`.

#### Start HTTP Server

The HTTP server exposes stored candles through REST endpoints. Use this when
you want another app or UI to read candle data from the local database.

Load a sample CSV and start the server:

```bash
go run ./cmd/server -load data/AAPL_sample.csv -symbol AAPL
```

Start the server when data already exists:

```bash
go run ./cmd/server
```

Query endpoints:

```bash
curl http://localhost:8080/stocks/ITC/latest
curl 'http://localhost:8080/stocks/ITC/candles?from=2025-01-01&limit=10'
```

#### Inspect PostgreSQL

Use this when you want to verify that candles were synced or inspect persisted
live-scan results.

Open `psql` inside the Docker container:

```bash
docker exec -it stock-market-analysis-postgres psql -U postgres -d stocks
```

Useful SQL:

```sql
\dt

-- Candles
SELECT COUNT(*) FROM candles;

SELECT symbol, COUNT(*), MIN(timestamp), MAX(timestamp)
FROM candles
GROUP BY symbol
ORDER BY symbol;

SELECT *
FROM candles
WHERE symbol = 'ITC'
ORDER BY timestamp DESC
LIMIT 10;

-- Scan results (written by live-scan after every run)
SELECT scanned_at, symbol, price, score, trend, rr, rel_strength, is_new, streak
FROM scan_results
ORDER BY scanned_at DESC, score DESC
LIMIT 50;

-- Signals for a specific symbol over time
SELECT scanned_at, price, score, rr, rel_strength, streak
FROM scan_results
WHERE symbol = 'HDFCBANK'
ORDER BY scanned_at DESC
LIMIT 20;
```

#### Run Tests

```bash
go test ./...
```

### Offline Scanner Flags

Available flags:

| Flag | Default | Description |
|---|---|---|
| `--top` | `5` | Number of top candidates to print |
| `--mode` | `swing` | Scanner mode: `swing`, `breakout`, or `all` |
| `--min-rr` | `2.0` | Minimum risk/reward ratio |
| `--db` | `false` | Scan candles from PostgreSQL |
| `--symbols` | `config/symbols.txt` | Symbol file for `--db` |
| `--period` | `2y` | DB history window (`2y`, `6m`, `90d`) |
| `--csv` | _none_ | Scan one local OHLCV CSV |
| `--csv-dir` | _none_ | Scan all CSV files in a folder |
| `--symbol` | CSV filename | Symbol for `--csv`, or single-symbol filter for `--db` |
| `--ema-margin` | `1.0` | Minimum % gap required between price and EMA200; `0` disables |
| `--min-volume` | `0` | Minimum 20-day avg daily volume; `0` disables (e.g. `200000`) |
| `--min-resistance-touches` | `2` | Minimum touches for a resistance zone to qualify; `1` allows all |
| `--max-ema10-extension` | `8.0` | Maximum % above EMA10 before filtering as extended; `<0` disables |
| `--max-ema50-extension` | `15.0` | Maximum % above EMA50 before filtering as extended; `<0` disables |
| `--max-support-extension` | `5.0` | Maximum % above support high before filtering as extended; `<0` disables |
| `--max-10d-move` | `12.0` | Maximum 10-candle % move before filtering as extended; `<0` disables |
| `--max-breakout-distance` | `3.0` | Maximum % below resistance for breakout watch candidates; `<0` disables |
| `--rs-lookback` | `20` | Swing relative-strength lookback vs benchmark; `0` disables |
| `--min-rs-pct` | `0` | Minimum stock outperformance vs benchmark over `--rs-lookback` |
| `--rs-symbol` | `NIFTY50` | Benchmark symbol for relative-strength filter |
| `--show-filtered` | `false` | Print skipped-symbol EMA/trend diagnostics and data errors |

Example output:
```
╔══════════════════════════════════════╗
║      Top Watchlist Candidates        ║
║  (research only — not buy signals)   ║
╚══════════════════════════════════════╝

1. HDFCBANK
   Score:      86.5 / 100
     Trend:   40.0 / 40
     R/R:     22.5 / 30
     Support: 20.0 / 20
     Volume:  4.0 / 10
   Price:      786.85
   Trend:      bullish
   R/R:        2.80  (good)
   Support:    760.00 – 765.50  (3 touches)
   Resistance: 820.00 – 828.00  (2 touches)
   Reasons:
     • Price above EMA50 (742.10) and EMA200 (698.30)
     • Risk/Reward 2.80 exceeds minimum 2.00
     • Support zone touched 3 times
     • Trade quality: good
```

> **Note:** Output is for watchlist research only, not buy recommendations.

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
| `KITE_API_KEY` | _(required for Kite sync)_ | Kite Connect API key |
| `KITE_API_SECRET` | _(required for token exchange)_ | Kite Connect API secret |
| `KITE_ACCESS_TOKEN` | _(required for Kite sync)_ | Kite Connect daily access token |
| `KITE_BASE_URL` | `https://api.kite.trade` | Kite Connect API base URL |

---

## Running Tests

```bash
go test ./...
```

```
ok  github.com/sahiltyagi27/stock-market-analysis/config
ok  github.com/sahiltyagi27/stock-market-analysis/internal/analysis
ok  github.com/sahiltyagi27/stock-market-analysis/internal/kite
ok  github.com/sahiltyagi27/stock-market-analysis/internal/loader
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
- [x] M5.1 — Kite Connect sync + multi-mode scan CLI (CSV / DB)
- [x] Live Scan — real-time scanner via Kite WebSocket (full mode, configurable interval, market hours guard)

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
