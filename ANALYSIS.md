# Strategy Analysis — Swing vs Crossover, Exits, Regime & Portfolio Constraints

_Investigation date: June 2026. Backtest window: 2022-01-01 → 2025-12-31, NSE Nifty 500 universe (~500 symbols), daily candles from Kite._

This document records the full analysis we ran to evaluate and improve the
trading strategies in this repo. Read top-to-bottom it tells the story; the
**Conclusions** section at the end is the actionable summary.

---

## Strategy-Health Regime Filter (Major Finding)

The single strongest result in the project, and the one to read first.

### Motivation
Traditional market-regime filters failed. Tests using `NIFTY close > EMA200` and
`NIFTY EMA50 > EMA200` (and a breadth variant, §5) reduced returns while
providing little drawdown protection. The strategy's performance was **not**
primarily driven by index direction — the index can look healthy (2024 was
broadly fine) while the strategy's *selected stocks* bleed.

### Hypothesis
The strategy itself may be a better regime indicator than the market. Instead of
asking *"Is the market healthy?"*, ask *"Is the strategy currently working?"*

### Implementation
A strategy-health gate monitors the rolling average realised **R of recently
closed trades** and pauses new entries when recent expectancy turns negative.
Open positions are unaffected. Default configuration:

- Window: **20 trades**
- Condition: **average R > 0**

### Results (full stack, 2022–2025)
| | CAGR | Max DD | Profit factor |
|---|---|---|---|
| Baseline (no gate) | 12.0% | −17.9% | 1.95 |
| **Health gate (W20)** | **14.7%** | **−12.5%** | **2.58** |

Per-year, it leaves good years untouched (2023 identical) and roughly halves the
losing ones (2024 −14→−8.5, 2025 −6→−3.2).

### Robustness
Window sweep — the improvement is not isolated to a single parameter:

| Window | Result |
|---|---|
| W12 | whipsaws (too few trades) |
| W15 | works |
| **W20** | **best** |
| W25 | works |
| W30 | works |
| W40 | works |

### Out-of-sample validation
Parameters frozen (W20, avgR > 0, risk 1%, max-weight 25%), each half held out:

| Period | No gate | Health gate |
|---|---|---|
| 2022–2023 (good) | +17.7% | +17.5% |
| 2024–2025 (weak) | −4.9% | −2.2% |

The gate stays inactive in favorable conditions and reduces losses in
unfavorable ones — it generalises in both directions.

### Cold-start deployment
A live problem: a fresh instance has no trade history and cannot evaluate health,
so it would start blind. **Solution: seed the gate with historical closed trades**
(`HealthSeed` / `--health-warmup-from`; live, load the last N from the DB).

Mid-2024 deployment test (after a weak H1):

| | Return | Max DD |
|---|---|---|
| Cold start | −7.1% | −11.5% |
| **Seeded** | **−1.1%** | **−5.8%** |

The seed sharply reduces early-regime losses, and is neutral when prior history
is favorable (seed = cold start).

### Conclusion
The strategy-health gate is the most robust risk-control mechanism discovered in
the project. Unlike market-direction filters, it adapts directly to the
strategy's *realised* performance. Current default configuration:

- Risk per trade: **1%**
- Max position weight: **25%**
- Health window: **20 trades**, average R > 0
- Seeded historical expectancy (no cold-start blindness)

_Full chronological detail in §9._

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

### Engine determinism fix (important)
While testing the crossover path we found the portfolio backtest was
**non-deterministic** — the same config gave +1.5% one run, −2.4% the next.
Cause: same-day candidate signals were sorted by score, but the candidate slice
is built by ranging a `map` (random order), so **tied-score signals filled the
5 slots in a random order**, cascading (via the gate and slot constraints) into
different trade sequences. Fixed with a `symbol` tiebreak in the sort — every
result is now reproducible. (Swing was affected too, just less — it has fewer
tied signals than the crossover flood.)

### Close-strength filter on crossover — looks good at one point, but NOT robust
Idea: only take a crossover when the signal candle closes in the upper part of
its range — `(close − low) / (high − low) ≥ X` — a conviction filter to reject
faded breakouts, paired with the EMA-hold exit (let winners run, no resistance
target). Tested on crossover + health gate + risk-1% + costs.

At **X = 0.5** it looked excellent — kept the 2023 bull (+44% vs +61% unfiltered)
*and* lifted the choppy 2024 (+1.5% → +10.1%) with lower drawdowns. **But it is
not a robust parameter:**

| From | X=0.50 | X=0.55 | X=0.60 |
|---|---|---|---|
| 2023 | +44.4% (114 trades) | −4.2% (24) | −2.8% (23) |
| 2024 | +10.1% | −1.1% | +6.4% |

A **0.05 change** collapses 2023 from +44% to −4% (a sheer cliff at 0.50→0.55),
and 2024 bounces non-monotonically (+10 → −1 → +6). That jaggedness is the
**signature of overfitting**, driven by path-dependency (filter × health-gate ×
slot constraint): a tiny change in which signals pass cascades into a completely
different trajectory. Contrast the genuinely robust levers (risk sizing,
health-window) which showed **smooth plateaus**. The X=0.5 result is a lucky
spike, not a trustworthy edge.

**Verdict:** the `--co-min-close-strength` flag is kept as an **experimental**
option (default off) but is **not** a validated improvement. Crossover remains a
regime-dependent momentum strategy that bleeds in bears (2022 ≈ −38%). This is a
good example of the rigorous setup catching a good-looking idea before it became
a bad live bet.

---

## 10. Mean reversion (RSI-2 oversold dip-buy) — REJECTED (PR: mean-reversion-v1)

### Hypothesis
Every strategy here is momentum/trend (swing pullback-in-uptrend, crossover),
and all of them stall in non-trending regimes. The proposal was a *regime
switcher* whose "defensive compounder" leg is a **mean-reversion** mode —
structurally orthogonal to momentum — to earn its keep in the weak 2024–2025
years where the index bled. We have no fundamentals (price/volume only), so the
"quality" screen is a price proxy: only buy dips in names above their long-term
mean.

### Design (Mean Reversion V1, Connors RSI-2 style)
- **Trend filter:** Close > EMA200 (buy dips only in structurally healthy names).
- **Oversold trigger:** RSI(2) < 10 (a sharp short-term washout).
- **Target:** EMA10 — revert to the short-term mean (engine `--exit-mode target`).
- **Stop:** Close − 2.5×ATR(14), deliberately wide so the mean/time exit leads.
- **Time stop:** `--max-hold 10`. Same portfolio engine as swing (5 slots,
  risk-1% sizing, 0.25% cost + 0.20% slip) for an apples-to-apples comparison.

### Per-year results vs the current swing strategy
| Year | MeanRev V1 | Swing (current) |
|---|---|---|
| 2022 | −15.5% | +6.7% |
| 2023 | +7.4% | +24.2% |
| 2024 | **−13.1%** | −4.9% |
| 2025 | **−12.5%** | −5.1% |
| 2026-YTD | **−19.5%** | +0.2% |
| **Full 22–26** | **−43.6%** (−46% DD) | **+30.5%** (−10% DD) |

Win rate was *high* (58–74%, textbook mean-reversion) but **profit factor < 1 in
every losing year**: snap-back wins are tiny, the ATR stop is wide, and the
asymmetry sinks the expectancy. The thesis is not merely unproven — it is
inverted: the mode is **worst in exactly the weak years it was meant to rescue.**

### Robustness — the rejection is airtight
- **+ health gate (window 20):** the gate *shuts it off* (24 trades in 4.5y) and
  it is still −11.6% full-period — the gate confirms there is no edge to trade.
- **Parameter sweeps (2024–2025):** wider target EMA20 → −31%; tighter stop
  1.5×ATR → −63.6%; stricter RSI<5 → −21.9%. **Every** direction is negative;
  there is no parameter neighbourhood where it works (so it is not a tuning miss).

### Verdict
Rejected. Root cause: oversold Indian names in 2024–2025 **kept falling**
(downside trend-persistence), so dip-buying caught falling knives even above
EMA200 — mean reversion needs choppy markets that round-trip, not one-way bleeds.
This also closes the regime-switcher's "defensive compounder" leg on the
evidence. The code (`--mode meanrev`, `internal/meanrev`) is kept in-tree but
clearly labelled REJECTED so the experiment is not blindly repeated; the one
reusable by-product is the **Wilder RSI helper** (`analysis.RSI`).

---

## 11. The health gate was a one-way door — shadow fix + the 45-day window odds (PR: health-gate-shadow)

### The bug
The strategy-health gate (§9) blocks new entries when the last N closed trades
average R < 0. But once it closes, **no new trade ever closes → the rolling
window never refreshes → the gate can never reopen.** A continuous 2022→2026 run
locks into cash in early 2024 and never trades again. Per-year backtests hid this
because each fresh run starts in warmup grace; the continuous run reveals it:

| Full 2022–26, risk 1% | 2022 | 2023 | 2024 | 2025 | 2026 | Total | MaxDD |
|---|---|---|---|---|---|---|---|
| gate ON (broken) | 15 | 51 | 9 | **0** | **0** | +30.5% | −9.8% |
| gate OFF | 15 | 51 | 33 | 31 | 11 | +10.1% | −26.6% |

The signals existed (33/31/11 in 2024–26) — the gate blocked every one. Note the
lockout was *accidentally protective* here (2024–25 stayed bad, so not-trading
beat trading). But a gate that can **never** reopen is structurally unsound: the
day a real bull returns it stays in cash and never knows.

### The fix: shadow trading (`--health-shadow`)
While the gate is closed the strategy keeps *simulating* the trades it would take
— **shadow positions that use no capital but feed their realised R into the
health window** — so the gate reopens when hypothetical recent performance turns
healthy. This is the textbook equity-curve filter; ours was missing the "keep
measuring while flat" half. Window-size sweep (fixed gate, full period, risk 1%):

| health-window | Return | MaxDD | Trades | Behaviour |
|---|---|---|---|---|
| 10 | +14.9% | −11.0% | 59 | **flaps** — cuts 2023 to 29 trades *and* re-enters 2025: worst of both |
| **20 (default)** | **+31.1%** | −9.8% | 76 | re-engages early 2024, correctly stays out 2025–26 |
| 30 | +28.6% | −9.8% | 77 | same as 20, marginally worse |

Window 20 is the sweet spot; smaller is noisier, not calmer. The fixed gate
re-engages *conditionally* on shadow performance — it dipped into early-2024,
found it still bad, and correctly stayed out of 2025–26. (A map-iteration
nondeterminism the shadow exits introduced was fixed by processing position
exits in sorted-symbol order, so the realised-R append order is reproducible.)

### The 45-day window odds — answering "can we get 5% in 45 days?"
Using the engine's **real daily equity curve** (`--equity-output`, exact — not a
per-trade reconstruction, which overstated returns by ~20% at risk 1% and ~2.4×
at risk 2%), rolling 45-day windows on the fixed gate:

| Risk 1% window set | ≥+5% | ≥+3% | >0 | median |
|---|---|---|---|---|
| All starts | 8.6% | 13.8% | 23% | +0.00% |
| Regime ON | 10.5% | 14.9% | 25% | +0.00% |
| **Regime ON + engaged** | **23.9%** | 33.8% | 51% | +0.13% |

By window-start year (≥+5%): 2021 0% · 2022 5.6% · **2023 36.4%** · 2024 0% ·
2025 0% · 2026 0%. **+5%/45d is essentially a 2023 (trending-regime) phenomenon.**

**Risk 2% does not help:** ≥+5% barely moves (23.9%→24.8%) but the median goes
**negative (−0.45%)** and the worst window deepens. Leverage adds variance and
downside, not +5% windows.

**Deployment rule:** +5%/45d is a ~1-in-4 *conditional* outcome — only when the
regime is on (NIFTY > EMA200) and the system is actually engaged — concentrated
in trending regimes; the *typical* engaged window is ≈flat. It is an occasional
upside harvest, not a reliable monthly cadence, and cannot be manufactured with
risk. The shadow fix matters precisely because it lets the system *be there* when
the next 2023 arrives instead of being locked in cash.

> **Known follow-up:** the live `cmd/paper-trade` gate reads `RecentTradeR` from
> the DB and has the **same one-way-door flaw**. Fixing it needs DB-backed shadow
> positions persisted across days — a separate change. Until then, a paper gate
> that closes after ≥20 trades must be re-seeded manually to reopen.

---

## 12. Conclusions (entry & exit)

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
10. **Mean reversion is not the missing ingredient (§10).** A textbook RSI-2
    dip-buy — the orthogonal, non-momentum bet meant to carry the weak regime —
    loses every year and is *worst* in 2024–2025, the exact years it targeted.
    Rejected; the regime-switcher's "defensive compounder" leg is closed on the
    evidence. Momentum-vs-mean-reversion was never the lever.

---

## 13. Open questions / next steps

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
risk-based sizing — the breakthrough** (§9); **mean reversion (§10, rejected —
closes the regime-switcher's defensive-compounder leg)**._

---

## 14. Reproduce

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

# §10 — mean reversion (REJECTED — loses every year, worst in the weak regime)
go run ./cmd/backtest --portfolio --mode meanrev --exit-mode target --max-hold 10 \
  --from 2024-01-01 --to 2025-12-31 \
  --cost-pct 0.25 --slippage-pct 0.20    # -13% / -13%; gate only shuts it off

# §11 — fixed gate (shadow), and the daily equity curve for window analysis.
# Without --health-shadow the continuous run locks into cash in early 2024.
go run ./cmd/backtest --portfolio --mode swing --from 2022-01-01 --to 2026-06-01 \
  --health-window 20 --health-shadow --equity-output /tmp/eq.csv
```

_Note: the `--exit-mode ema` / portfolio engine is the trustworthy path. The
serial single-symbol mode (no `--portfolio`) is kept for signal inspection only._
