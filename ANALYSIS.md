# Strategy Analysis — Swing vs Crossover, Exits, Regime & Portfolio Constraints

_Investigation date: June 2026. Backtest window: 2022-01-01 → 2025-12-31, NSE Nifty 500 universe (~500 symbols), daily candles from Kite._

This document records the full analysis we ran to evaluate and improve the
trading strategies in this repo. Read top-to-bottom it tells the story; the
**Conclusions** section at the end is the actionable summary.

---

## 1. The two strategies

### Swing (the original, support-zone strategy)
Entry on a pullback to a tested **support zone** in an up-trending stock.
Quality filters added over PRs (esp. #35):
- Trend: price > EMA50 and > EMA200, EMA200 rising (`--ema200-slope-period`)
- Risk: ATR-based stop; `--max-risk-pct 8`, `--min-risk-pct 1.5`
- Hard **bullish-candle** requirement (reject if signal candle closed red)
- R/R ≥ 2, resistance zone ≥ 2 touches, late-rally extension guards
- Native exit: fixed **target** at the nearest resistance midpoint

### Crossover (the momentum idea tested in this investigation)
Entry when **EMA7 crosses above EMA21** (fresh, within `--co-max-age` candles).
- SL = **Low of the candle before the crossover**, with a `--co-min-risk-pct 3` floor
  (use whichever is lower / wider)
- Target = nearest resistance ≥ `--co-min-target-pct` above entry (skip-too-close)
- Filters: `--co-min-rr 3`, `--co-min-vol-mult 3` (today's volume ≥ 3× 10-day avg)
- Native exit: fixed **target**

---

## 2. How we backtest

Walk-forward, no lookahead. For each signal day D: run the scanner on
`candles[:D]`, enter at the **open of D+1**, exit on SL / target / (later) other
rules. Pessimistic tie-breaking: if a candle hits both SL and target, SL wins.

Two capital models were used:
- **Serial (overlap-blind):** each trade deploys full capital, one at a time.
  Simple but **unrealistic** — assumes you can always take the next signal.
- **Portfolio-aware:** one shared capital pool, a cap on concurrent positions
  (`--max-positions`), equal-slice sizing. This is what a real account
  experiences. **This is the model to trust.**

---

## 3. The TATAPOWER reality check (why momentum entries mislead)

The user made ~**+22%** manually on TATAPOWER (bought ~₹331 in Feb 2025, sold
₹400+ in May) — one position, held through the trend.

The crossover system, same stock, same window, generated **5 separate trades**:

| # | Entry | Exit | Result |
|---|---|---|---|
| 1 | 12-Mar 360.50 | 362.62 | +0.6% (target hit immediately) |
| 2 | 19-Mar 374.80 | 349.00 | **−6.9%** (SL on the March dip the user held through) |
| 3 | 15-Apr 378.00 | 387.05 | +2.4% |
| 4 | 12-May 392.00 | 403.12 | +2.8% |
| 5 | 25-Jun 403.05 | 394.85 | −2.0% |

Net: **−3.4%** on a stock that ran +22%. Under the strict filters it took **0**
TATAPOWER trades at all (resistance overhead too close to clear the 8% target).

**Three structural failures, none fixed by a regime filter:**
1. **Late entry** — EMA7×21 confirms the turn only after ~9% of the move is gone.
2. **Fixed target caps the trend** — sold at the first resistance, repeatedly.
3. **Tight stop whipsaws** — stopped on the normal pullback the trend trader holds through.

This reframed the problem: it's not _when_ the market is up (regime), it's _how
you exit_. That launched the exit-method study.

---

## 4. Exit-method study (serial model)

Same crossover entries, four exit rules:
- **target** — fixed resistance (native)
- **EMA-hold** — hold until EMA7 crosses back below EMA21 (SL still applies)
- **ATR trail 3×** — no target cap, trailing stop at highestHigh − 3×ATR
- **partial** — half at target, half held to EMA-recross

### Total R by year (crossover, serial, no regime)

| Year | target | EMA-hold | ATR trail | Trades |
|---|---|---|---|---|
| 2022 | +18.90 | +21.60 | +2.87 | 39 |
| 2023 | +34.91 | +38.02 | +31.20 | 49 |
| 2024 | −10.92 | −5.19 | −9.52 | 30 |
| 2025 | −0.77 | +8.40 | +2.43 | 33 |
| **Total** | **+42.12** | **+62.83** | **+26.98** | 151 |

### Capital, ₹1 lakh all-in serial (crossover)

| Exit | Final capital | Total R |
|---|---|---|
| EMA-hold | ₹7.61L | +62.83 |
| Fixed target | ₹4.44L | +42.12 |
| Partial (½/½) | ₹3.98L | +39.17 |
| ATR trail 3× | ₹2.00L | +26.98 |

### Key insights
- **The fixed target is the biggest leak** — it caps explosive trends (VEDL ran
  +79%, target took +4.5R and quit; NETWEB ran +128%, target took +3R).
- **EMA-hold wins total but is fat-tail fragile.** Remove the top 1–2 trends and
  it collapses: 2022 EMA-hold +21.60 → −7.65 without NMDC+YESBANK; 2025 +8.40 →
  −10.79 without VEDL. The fixed target's edge is spread across the body and
  survives removing the monsters.
- **ATR trailing is dominated** — the most explosive trends are the most volatile,
  so a volatility-based trail gets shaken out early (NETWEB: EMA-hold +8.51R vs
  trail +1.15R). Across 4 years it's the worst exit.
- **Partial exit is a dud** — slightly worse than pure target; the half-runner
  dilutes monster capture while still giving back medium winners.

---

## 5. Regime filter test (breadth proxy)

We had no NIFTY index data, so we used a **market-breadth proxy**: regime is "on"
when ≥50% of all 500 stocks have EMA20 > EMA50 on the entry day. Trades on
"off" days are skipped.

### Result (crossover, serial) — it made everything WORSE

| Exit | No regime | With regime |
|---|---|---|
| Fixed target | ₹4.44L | ₹2.71L |
| EMA-hold | ₹7.61L | ₹6.67L |
| ATR trail | ₹2.00L | ₹1.56L |
| Partial | ₹3.98L | ₹3.19L |

It removed 46 of 151 trades. Crucially, per year:

| Year | Total | Removed |
|---|---|---|
| 2022 | 39 | 10 |
| 2023 | 49 | 16 |
| 2024 | 30 | **4** |
| 2025 | 33 | 16 |

**It barely touched 2024 (the only losing year) and gutted the good years.** The
market was broadly up in 2024, so breadth never flagged it — yet the trades lost
anyway. Conclusion: **market direction is the wrong thing to filter on.** This
echoes TATAPOWER (a strong stock won in a weak market) from the opposite side (a
strong market still produced losing trades).

The untested alternative — **relative strength** (stock vs NIFTY), which keeps
leaders and cuts laggards regardless of market direction — remains the only
entry filter the data still points to. It needs NIFTY index candles, which
`kite-sync` doesn't fetch (it only pulls EQ instruments).

---

## 6. Swing strategy, same study (serial)

The original swing strategy had **never** been tested multi-year with these exits.

### Capital, ₹1 lakh all-in serial (swing, 126 trades, min-score 60)

| Exit | No regime | With regime |
|---|---|---|
| EMA-hold | ₹5.34L (+63.8R) | ₹5.30L |
| ATR trail | ₹2.47L | ₹1.42L |
| Partial | ₹2.85L | ₹3.38L |
| Fixed target | **₹1.62L (+21.2R)** | ₹2.94L |

**The original swing strategy, as built (fixed target), made only ₹1.62L over 4
years** — barely above a fixed deposit. Switching only the exit to EMA-hold took
it to ₹5.34L (3.3×). _The exit mattered more than the entry._

Both strategies' EMA-hold R were nearly tied (swing +63.8, crossover +62.8) —
the choice of entry signal mattered far less than the exit.

---

## 7. Portfolio-aware backtest (the decisive test)

The serial model is overlap-blind: it assumes infinite capital and ignores that
long EMA-holds tie up money. We built a **portfolio engine** (`internal/backtest/portfolio.go`,
`--portfolio` flag): one ₹1 lakh pool, `--max-positions 5`, equal-slice sizing,
mark-to-market equity, drawdown tracking, same-day gap-down stops.

### Result — 5 concurrent positions, 2022→2025

| Strategy | Exit | Final | CAGR | Max DD | Win% | Trades |
|---|---|---|---|---|---|---|
| **Swing** | **EMA-hold** | **₹1.68L** | **13.9%/yr** | −16.2% | 31% | 110 |
| Crossover | EMA-hold | ₹1.39L | 8.6%/yr | −19.9% | 27% | 129 |
| Crossover | target | ₹1.36L | 8.0%/yr | −12.3% | 31% | 133 |
| Swing | target | ₹1.26L | 6.0%/yr | −27.3% | 35% | 109 |

### The two findings that matter

**1. The serial model was a mirage — and it inverted the ranking.**

| | Serial (overlap-blind) | Portfolio (5 slots) |
|---|---|---|
| Crossover + EMA | ₹7.61L | ₹1.39L |
| Swing + EMA | ₹5.34L | ₹1.68L |

Serial overstated returns ~4–5× **and** ranked crossover above swing. Under
realistic constraints, **swing wins.** Slots are the scarce resource; crossover
floods you with mediocre signals you can't all take, and its few monster trends
either can't get a slot or block five other trades for months.

**2. EMA-hold remains the best exit under constraints** — for swing, decisively
(₹1.68L vs ₹1.26L) and with a far shallower drawdown (−16% vs −27%).

### The humbling benchmark
NIFTY 50 buy-and-hold over 2022–2025 ≈ **~10%/yr** (~9% price + dividends),
~−15% drawdowns. Against that (frictionless):
- **Swing + EMA-hold (13.9%/yr, −16% DD): modestly beats the index.**
- **Crossover (8–8.6%/yr): _underperforms_ buy-and-hold.**

### Transaction costs make it real (and worse)
Frictionless backtests lie. Modeling NSE-delivery round-trip cost (0.25%:
brokerage + STT + fees) and slippage (0.20%/leg) — flags `--cost-pct`,
`--slippage-pct`:

| Strategy (portfolio, 5 slots) | Frictionless | With costs |
|---|---|---|
| Swing + EMA | 13.9%/yr (₹1.68L) | **9.4%/yr (₹1.43L)** |
| Crossover + EMA | 8.6%/yr (₹1.39L) | **2.4%/yr (₹1.10L)** |

- Costs hit **crossover ~3× harder** (−6.2 pts/yr vs swing's −4.5): it trades more
  (129 vs 110) and its per-trade winners are smaller, so fees eat a bigger slice.
- Drawdowns deepen (slippage worsens every stop): swing −21.5%, crossover −27.7%.
- **Cost-adjusted, crossover (2.4%/yr) is near-worthless vs the index (~10%/yr),
  and even swing (9.4%/yr) only roughly _matches_ buy-and-hold — with a deeper
  drawdown.** Trading less (or not at all) is a serious benchmark.

---

## 8. Conclusions

1. **Winner: the original swing strategy + EMA-recross exit** — frictionless
   ~14%/yr at −16% DD; **cost-adjusted ~9.4%/yr at −21% DD**, which only roughly
   *matches* the index. The first thing we built, with a better exit, beat the
   fancy new idea — but barely beats doing nothing once costs are real.
2. **Crossover is not worth pursuing standalone** — even frictionless it lags the
   index, and **cost-adjusted it collapses to ~2.4%/yr**. It trades too much.
3. **EMA-recross hold > fixed target** — validated across 2 strategies × 4 years
   × serial & portfolio. The fixed target was strangling both strategies.
4. **Drop:** ATR trailing exit, partial exit, and the market-breadth/absolute
   regime filter — all dominated or counterproductive.
5. **Retire the overlap-blind serial backtest** — it is actively misleading.

---

## 9. Open questions / next steps

- **Relative-strength entry filter** (stock vs NIFTY) — the only entry filter the
  data still endorses; needs NIFTY 50 index candles (separate fetch, not EQ).
- **`--max-positions` sensitivity** (3 / 5 / 8 / 10) — slots are the binding
  constraint; the optimum is unknown.
- **Why did 2024 lose across the board?** Exit-independent; worth a dedicated look.
- **Beat the index at all?** Cost-adjusted swing only ties NIFTY. Open question
  whether any refinement (relative strength, lower turnover, fewer/larger
  positions) yields a durable edge over buy-and-hold.

_Done in this investigation: transaction-cost & slippage modeling
(`--cost-pct` 0.25, `--slippage-pct` 0.20) — see §7._

---

## 10. Reproduce

```bash
# Backfill data (daily, ≤5y per Kite request)
go run ./cmd/kite-token                 # refresh access token, paste into .env
go run ./cmd/kite-sync --period 5y

# Portfolio backtest — the winner (cost-adjusted by default: 0.25% + 0.20% slip)
go run ./cmd/backtest --portfolio --mode swing \
  --from 2022-01-01 --to 2025-12-31 \
  --min-score 60 --min-rr 2 \
  --exit-mode ema --max-positions 5 --max-hold 0 --capital 100000 \
  --cost-pct 0.25 --slippage-pct 0.20

# Crossover, for comparison
go run ./cmd/backtest --portfolio --mode crossover \
  --from 2022-01-01 --to 2025-12-31 \
  --min-score 80 --exit-mode ema --max-positions 5 --max-hold 0 --capital 100000 \
  --co-min-rr 3 --co-min-vol-mult 3 --co-min-target-pct 8 --co-min-risk-pct 3 \
  --cost-pct 0.25 --slippage-pct 0.20

# Frictionless (reproduces §1–§7 numbers): add --cost-pct 0 --slippage-pct 0
```

_Note: the `--exit-mode ema` / portfolio engine is the trustworthy path. The
serial single-symbol mode (no `--portfolio`) is kept for signal inspection only._
