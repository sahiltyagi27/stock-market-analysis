# Strategy Analysis — Swing vs Crossover, Exits, Regime & Portfolio Constraints

_Investigation date: June 2026. Backtest window: 2022-01-01 → 2025-12-31, NSE Nifty 500 universe (~500 symbols), daily candles from Kite._

This document records the full analysis we ran to evaluate and improve the
trading strategies in this repo. Read top-to-bottom it tells the story; the
**Conclusions** section at the end is the actionable summary.

---

## ⭐ Major finding — the Strategy-Health regime filter

The single strongest result in the project. After every *market*-based regime
filter failed, the breakthrough was to gate on the **strategy's own equity
curve**, not the market's.

- **Why market gates failed.** Breadth (§5), relative-strength (§8), and a
  NIFTY-trend gate (§9) all reduced returns or missed the losing years. The
  index can look healthy (2024 was broadly fine) while the strategy's *selected
  stocks* bleed. **Market direction is not the strategy's risk.**
- **Why strategy-health succeeds.** Gate new entries on recent realised
  expectancy: only trade when the last **20 closed trades** have mean R ≥ 0
  (`--health-window 20`). When the picked stocks start losing, it pauses —
  regardless of NIFTY.
- **Result (full stack, 2022–25):** CAGR **12.0 → 14.7%**, max DD **17.9 → 12.5%**,
  profit factor **1.95 → 2.58**. Good years untouched (2023 identical), losing
  years roughly halved (2024 −14→−8.5, 2025 −6→−3.2).
- **Robustness:** broad parameter plateau (W15–W40 all beat baseline; ≤12
  whipsaws). **Out-of-sample (frozen params):** harmless on the good half
  (2022–23: +17.7 vs +17.5), protective on the bad half (2024–25: −4.9→−2.2%/yr,
  DD −20.2→−10.5%).
- **Cold-start (live) — fixed.** A fresh deployment has no trade history, so the
  gate would start blind. Solved by **seeding** the window with the last N
  completed trades (`--health-warmup-from` in backtest; load from DB live). Demo:
  deploying mid-2024 after a weak H1, seeding turned −7.1% (DD −11.5%) into −1.1%
  (DD −5.8%). Harmless when prior history is good; protective when it is bad.

Detail in §9. **The default engine config is now:** swing + EMA-recross exit +
risk-1% sizing + max-weight 25% + health-window 20, costs on.

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

## 8. Relative-strength & sector-strength entry filters (PRs #38–#42)

After the core investigation, RS and sector-strength swing filters were added
(scanner `RelativeStrengthLookback`/`MinRelativeStrengthPct`,
`SectorStrengthLookback`/`MinSectorStrengthPct`; a NIFTY50 benchmark sync, and a
Nifty 500 stock→sector map). Both were swept against the portfolio baseline
(swing + EMA exit + costs, 5 slots, 2022–2025).

### Stock relative strength (stock vs NIFTY) — hurts
| Config | CAGR | Win% | Trades |
|---|---|---|---|
| no-RS (baseline) | **9.4%** | 29% | 110 |
| RS L20 min0 | 6.4% | 28% | 88 |
| RS L20 min5 | 5.0% | 34% | 67 |
| RS L50 min0 | 4.8% | 31% | 88 |

Every setting underperforms. Win rate rises as the filter tightens but total
return falls — it removes volatile winners. Cause: RS demands recent
*out*performance, while swing buys pullbacks (recent *under*performance). The two
are opposed; RS filters out the dip-buy setups the strategy relies on.

### Sector strength (mapped sector index vs NIFTY) — also hurts
Using the full Nifty 500 sector map (PR #42, 361 symbols):

| Config | CAGR | Max DD | Win% |
|---|---|---|---|
| no-sector (baseline) | **9.5%** | −21.8% | 29% |
| sector L20 min0 | 6.1% | −19.2% | 29% |
| sector L50 min0 | 8.1% | −20.5% | 31% |
| sector L75 min0 | 6.0% | −17.1% | 29% |
| sector L50 min3 | −1.9% | −20.7% | 27% |

Every setting underperforms too. (A partial 53-symbol map briefly showed a
+0.6 pt blip at L50/min0; the full 361-symbol map erased it — it was a mapping
artifact, a good reminder to test with complete data.)

### Verdict
Neither filter yields a durable edge; both reduce returns. The honest ceiling is
unchanged: **swing + EMA-recross + costs ≈ 9.5%/yr ≈ the index.** The
relative-strength / sector machinery — the most promising lever the analysis
identified — does **not** beat buy-and-hold on this data. A valuable negative
result: the added complexity is not paying off here.

---

## 9. Portfolio construction: allocation, position count, opportunity loss

The portfolio backtest pointed at *construction* (not signals) as the bigger
lever. Three experiments (swing + EMA + costs, 2022–2025):

### Variant D — leadership-ranked slot allocation (`--alloc-lookback N`)
Rank same-day candidates for free slots by N-candle leadership return (score as
tiebreak). Entries/exits unchanged — only *which* competing signals get funded.

| maxpos | alloc | CAGR | Max DD | PF | Trades |
|---|---|---|---|---|---|
| 5 | score | 9.4% | −21.8% | 1.95 | 110 |
| 5 | RS 100D | 9.4% | −21.5% | 1.95 | 110 |
| 3 | score | 8.8% | −32.0% | 1.97 | 80 |
| 3 | RS 100D | 9.9% | −31.9% | 2.07 | 79 |

At 5 slots, D does **nothing** (identical 110 trades) — the constraint rarely
forces a same-day choice, so re-ordering candidates changes nothing. At 3 slots
(constraint binds) D **helps** (CAGR 8.8→9.9%, PF 1.97→2.07). Concept validated,
but only where slots are genuinely scarce.

### Max-positions sweep
| maxpos | CAGR | Max DD |
|---|---|---|
| 3 | 8.9% | −32.0% |
| **5** | **9.4%** | −21.8% |
| 7 | 8.3% | −19.5% |
| 10 | 5.4% | −14.5% |

5 is the CAGR peak. Fewer = worse return *and* much deeper drawdown
(concentration risk > selection edge); more = lower return, smaller drawdown.

### M10 — opportunity-loss: does the slot limit cost anything?
When the portfolio is full, qualifying signals are rejected. Each rejected signal
was simulated with the same exit + costs and compared to the trades taken:

| | avg R:R | win% |
|---|---|---|
| Accepted (taken) | **+0.50R** | 29% |
| Rejected (full) | **−0.12R** | 33% |

**Rejected signals were *worse* than accepted ones.** The slot limit isn't costing
you — the signals skipped while full would have lost money on average (likely
because "full" correlates with signal-rich, extended markets where marginal
signals are low quality).

**Implication: rotation (sell a holding to chase a rejected signal) would HURT —
it swaps +0.50R for −0.12R. Rotation is a dead end; not worth building.** One cheap
measurement killed a multi-week feature that would have reduced returns.

### M12 — risk-based ("ATR") position sizing (`--risk-pct`) — the breakthrough
Instead of equal 1/N slices, size each position so a stop-out costs a fixed % of
equity: `notional = equity × risk% ÷ ((entry−SL)/entry)`, capped at
`--max-weight-pct` (25%). Tight stop → larger position; wide/volatile stop →
smaller. Entries, exits, and trade set are unchanged — only capital allocation.

| Sizing | CAGR | Max DD | PF | Win% |
|---|---|---|---|---|
| equal-slice (baseline) | 9.5% | −21.8% | 1.95 | 29% |
| risk 0.5% | 5.9% | −9.7% | 1.95 | 29% |
| **risk 1.0%** | **12.1%** | **−17.9%** | 1.95 | 29% |
| risk 1.5% | 10.7% | −25.4% | 1.99 | 29% |
| risk 2.0% | 8.9% | −27.0% | 1.97 | 29% |

**risk 1.0% improves return AND drawdown simultaneously** (9.5→12.1% CAGR,
−21.8→−17.9% DD) — the only lever in the whole study to do both. Same win rate
and R:R (sizing doesn't change trades, only weights). It's a smooth hump (1.5%
also beats baseline CAGR; weight-cap 25–30% all ~12%), not a lonely spike.

Robustness: the **drawdown improvement holds across sub-periods** (2023–2025:
9.4% at −17.9% DD). The CAGR *uplift* is partly 2022-driven (2023–25 return ≈
baseline), but risk-adjusted it wins in every cut — full-period return/DD 0.68 vs
baseline 0.44. This is the first config to beat the index (~10%/yr) on return and
on risk-adjusted terms. **Mechanism: down-weighting volatile (wide-stop) names
and up-weighting tight-stop ones is genuinely better than equal weighting** —
portfolio construction, exactly where the edge was hiding.

### Per-year regime check (the essential caveat)
Risk-1% was promoted to the **default** (`--risk-pct 1.0`, `--max-weight-pct 25`).
But a year-by-year breakdown reveals the strategy's true character:

| Year | equal-slice | risk-1% | Regime |
|---|---|---|---|
| 2022 | +8.4% (DD −8.4%) | +10.4% (DD −7.9%) | choppy-up |
| **2023** | **+72.5% (DD −11.8%)** | **+72.9% (DD −9.3%)** | strong bull |
| 2024 | −17.1% (DD −19.5%) | −14.0% (DD −16.0%) | correction |
| 2025 | −9.8% (DD −9.9%) | −6.0% (DD −7.3%) | weak |

**2022 and 2023 were positive; 2024 and 2025 were negative.** The multi-year
return is dominated by 2023 (+73%), but 2022 also contributed — so it's
regime-dependent, not a single-year fluke. Still, two of four years lost money:
don't over-trust the headline CAGR.

What *is* robust: **risk-1% sizing improves drawdown in every single year** and
trims the losing ones (2024: −17.1→−14.0; 2025: −9.8→−6.0), giving up nothing in
the good years. Promoted as a **risk-control default**, not a return amplifier.

_(Note: an earlier draft of this table accidentally had the RS entry filter on
— it understated 2022 as negative. These are the clean RS-off numbers.)_

### Regime gate (NIFTY trend) — tested, does NOT help (`--regime`)
A market-level "should we trade at all?" switch: block new entries unless NIFTY
is in a healthy uptrend (`price`: close > EMA200; `ema`: EMA50 > EMA200).
Existing positions still exit normally.

| Gate | CAGR | Max DD |
|---|---|---|
| none | 12.1% | −17.9% |
| price (close > EMA200) | 9.2% | −17.0% |
| ema (EMA50 > EMA200) | 3.6% | −17.1% |

**Both gates cut return while barely improving drawdown.** Per-year, the gate
only slightly trims 2024 (−14.0→−13.0) but cuts the good years far more
(2023 +72.9→+58.9). Why: **NIFTY held above its 200-EMA through much of 2024's
decline** — the index stayed "healthy" while the individual stocks the strategy
picked fell. Same lesson as the breadth (§5) and RS (§8) filters:
**market-direction is not the strategy's actual risk.** Kept as a default-off
diagnostic (`--regime`), not promoted.

### Strategy-health gate (equity-curve filter) — THE WIN (`--health-window`)
Instead of asking "is the *market* healthy?", ask "is the *strategy* working?"
Only open new positions when the last N closed trades show positive expectancy
(`avgr`: mean R ≥ 0, or `pf`: profit factor ≥ threshold). Purely causal — uses
only realised, closed-trade R up to the decision day.

| Config | CAGR | Max DD | PF |
|---|---|---|---|
| no gate | 12.0% | −17.9% | 1.95 |
| **health avgR>0, W20** | **14.7%** | **−12.5%** | **2.58** |
| health PF>1.2, W30 | 13.9% | −15.0% | 2.26 |

**Higher return, lower drawdown, higher profit factor — all at once.** Per-year,
it leaves the good years untouched and roughly halves the losing ones:

| Year | no gate | health-W20 |
|---|---|---|
| 2022 | +11.1% | +10.4% |
| 2023 | +72.9% | +72.9% (identical) |
| 2024 | −14.0% (DD −16%) | **−8.5% (DD −10.5%)** |
| 2025 | −6.0% (DD −7.3%) | **−3.2% (DD −4.9%)** |

When the strategy is working the gate stays open (2023 literally unchanged); when
the *selected stocks* start bleeding (negative recent expectancy) it pauses —
**regardless of what NIFTY is doing.** This is why it succeeds where the market
gates (§5/§8/§9-regime) failed: it measures the strategy's *own* risk, not the
market's. Robust across a broad window plateau (W15–W40 all beat baseline; W20
peak). Short windows (≤12) whipsaw — too few trades, reacts to noise.

**Out-of-sample (frozen params, split-half):** harmless on the good half
(2022–23: +17.7% no-gate vs +17.5% gated) and protective on the bad half
(2024–25: −4.9→−2.2%/yr, DD −20.2→−10.5%). It generalises in both directions.

**Cold-start — FIXED.** A fresh deployment has no trade history, so the gate would
start blind and take early losses. Fixed by **seeding** the window with prior
closed-trade R (`HealthSeed`; CLI `--health-warmup-from` runs a warmup pass;
live, load the last N trades from the DB). Demonstration — deploying 2024-07
after a weak H1: cold start −7.1% (DD −11.5%, 17 blind trades) vs seeded −1.1%
(DD −5.8%, 6 trades). When prior history is good the seed equals cold start
(harmless); when bad it starts the gate closed (protective).

**Default:** `--health-window 20` (avgR ≥ 0) is now on by default; `0` disables.

**Residual caveat:** the gate still relies on in-flight positions closing to
update the window; a prolonged flush that closes everything as losses could keep
it shut until the next signal it lets through. A live system may add a time-based
reopen or a tiny always-on probe. Not observed in 2022–25.

The losing-years problem is finally addressed — not by a market gate, but by the
strategy watching its own equity curve.

---

## 10. Conclusions (entry & exit)

1. **Winner: the original swing strategy + EMA-recross exit** — frictionless
   ~14%/yr at −16% DD; **cost-adjusted ~9.4%/yr at −21% DD**, which only roughly
   *matches* the index. The first thing we built, with a better exit, beat the
   fancy new idea — but barely beats doing nothing once costs are real.
2. **Crossover is not worth pursuing standalone** — even frictionless it lags the
   index, and **cost-adjusted it collapses to ~2.4%/yr**. It trades too much.
3. **EMA-recross hold > fixed target** — validated across 2 strategies × 4 years
   × serial & portfolio. The fixed target was strangling both strategies.
4. **Drop:** ATR trailing exit, partial exit, the market-breadth/absolute regime
   filter, **and the stock-RS / sector-strength entry filters (§8)** — all
   dominated or counterproductive.
5. **Retire the overlap-blind serial backtest** — it is actively misleading.
6. **Portfolio construction > signal tuning (§9).** `max-positions 5` is the CAGR
   peak; RS-allocation only helps under scarce slots; opportunity-loss (M10) rules
   out rotation. **Risk-based position sizing (M12) is now the default
   (`--risk-pct 1.0`, `--max-weight-pct 25`):** it lifts full-period CAGR
   9.5→12.1%, cuts drawdown 21.8→17.9%, and — critically — **improves drawdown in
   every individual year**. Default config: **swing + EMA exit + risk-1% sizing +
   max-positions 5 + costs**, with RS/sector entry filters off across all CLIs.
7. **The headline return is regime-dependent.** Per-year (§9), 2022 (+10%) and
   2023 (+73%) were positive; 2024 (−14%) and 2025 (−6%) lost. Risk-sizing makes
   the losing years less bad but can't manufacture an edge.
8. **Market-direction gates all fail (breadth §5, RS §8, NIFTY-trend §9).** The
   index stayed healthy while the strategy's stocks fell in 2024 — market
   direction is not the strategy's risk. That whole idea is exhausted.
9. **The strategy-health gate (equity-curve filter) is the answer (§9).**
   `--health-window 20` (only trade when the last 20 closed trades have avg R ≥ 0)
   lifts CAGR 12.0→14.7%, cuts drawdown 17.9→12.5%, raises PF 1.95→2.58, and
   roughly halves the losing years while leaving the good ones untouched. The
   strategy reading its *own* expectancy beats any market proxy. Robust across
   W15–W40. **Recommended addition to the default config.**

---

## 11. Open questions / next steps

- **Volatility regime (not market-trend).** The NIFTY-trend gate failed (§9), but
  a *volatility* filter is untested — e.g. no new entries when NIFTY ATR20/price
  exceeds a threshold (sideways-volatile markets chop swing trades apart). This is
  the remaining regime idea worth trying; market *direction* is exhausted.
- **Cross-sectional RS rank (Variant C)** — "is this among the strongest stocks?"
  (percentile rank of 50–100D return across all 500), distinct from the
  time-series RS filters that failed in §8. Test as a universe filter, not a tiebreak.
- **Turnover reduction** — costs are a persistent drag; anything that lifts profit
  factor without adding trades is interesting.

_Done: transaction costs (§7); RS/sector filters (§8, negative); portfolio
allocation, max-positions, M10 opportunity-loss (rotation ruled out), and **M12
risk-based sizing — the breakthrough** (§9)._

---

## 12. Reproduce

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

# §8 — relative-strength / sector-strength sweeps (both underperform baseline)
go run ./cmd/backtest --portfolio --mode swing --from 2022-01-01 --to 2025-12-31 \
  --min-score 60 --min-rr 2 --exit-mode ema --max-positions 5 --max-hold 0 \
  --capital 100000 --cost-pct 0.25 --slippage-pct 0.20 \
  --rs-lookback 20 --min-rs-pct 0                       # stock RS (hurts)
go run ./cmd/backtest --portfolio --mode swing --from 2022-01-01 --to 2025-12-31 \
  --min-score 60 --min-rr 2 --exit-mode ema --max-positions 5 --max-hold 0 \
  --capital 100000 --cost-pct 0.25 --slippage-pct 0.20 \
  --sector-map config/sector-map.csv --sector-rs-lookback 50 --min-sector-rs-pct 0  # sector (hurts)

# §9 — portfolio construction
#   --alloc-lookback N   rank same-day candidates by N-candle leadership return
#   --max-positions K    concurrent-position cap (5 = CAGR peak)
# The run also prints an "Opportunity loss" block (M10) whenever the portfolio
# fills up: rejected-signal avg R:R vs accepted — used to rule out rotation.
go run ./cmd/backtest --portfolio --mode swing --from 2022-01-01 --to 2025-12-31 \
  --min-score 60 --min-rr 2 --exit-mode ema --max-positions 5 --max-hold 0 \
  --capital 100000 --cost-pct 0.25 --slippage-pct 0.20 --alloc-lookback 100

# §9 / M12 — BEST CONFIG TO DATE: risk-based sizing (12.1%/yr, −17.9% DD)
go run ./cmd/backtest --portfolio --mode swing --from 2022-01-01 --to 2025-12-31 \
  --min-score 60 --min-rr 2 --exit-mode ema --max-positions 5 --max-hold 0 \
  --capital 100000 --cost-pct 0.25 --slippage-pct 0.20 \
  --risk-pct 1.0 --max-weight-pct 25
```

_Note: the `--exit-mode ema` / portfolio engine is the trustworthy path. The
serial single-symbol mode (no `--portfolio`) is kept for signal inspection only._
