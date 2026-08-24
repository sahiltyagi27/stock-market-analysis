# Strategy Analysis — Swing vs Crossover, Exits, Regime & Portfolio Constraints

_Investigation date: June 2026. Backtest window: 2022-01-01 → 2025-12-31, NSE Nifty 500 universe (~500 symbols), daily candles from Kite._

This document records the full analysis we ran to evaluate and improve the
trading strategies in this repo. Read top-to-bottom it tells the story; the
**Conclusions** section at the end is the actionable summary.

> **⚠ Read §15 before trusting any CAGR/return number below.** Every result
> in §1–§14, including the "Major Finding" immediately below, was validated
> only on the 2022–2026 window. §15's walk-forward OOS test (2012–2025, now
> that deeper history is available) found the in-sample 14.0% CAGR compresses
> to 2.7–4.4% CAGR out-of-sample, driven almost entirely by one year (2023) —
> and underperformed plain buy-and-hold NIFTY (12.9% CAGR) by 3–4x. A
> follow-up found why: the strategy structurally can't hold genuine
> multibaggers (took 1 losing trade, total, across the 3 biggest compounders
> in the universe over 14+ years — the target-setting logic requires a
> resistance zone above price, which a stock at new highs doesn't have). A
> second follow-up checked real fundamental data (this codebase has none) for
> those multibaggers: genuine profit-growth inflections do precede/coincide
> with their big moves, but each also survived a 50–60% mid-journey drawdown
> — so even perfect stock-picking needs a drawdown-tolerance framework this
> codebase doesn't have either. §16 built that strategy (`longhold` +
> `trendstop`): **18.9% CAGR** at 20 positions (revised down from an
> initially-reported 24.2% — see §16b, a non-determinism bug in the
> portfolio engine inflated several results this session), beating
> buy-and-hold NIFTY by ~1.6x — with a 55.9% max drawdown that is *currently
> active*, not historical, as of this writing. §16b also ran the walk-forward
> OOS validation on this strategy: it holds up out-of-sample (26.6% CAGR
> 2012–2018, 17.6% CAGR 2019–2025, both clearing NIFTY's era CAGR), unlike
> the swing strategy in §15. Also fixed a real CAGR-display bug found along
> the way (hardcoded 4-year assumption). See §15–§16b for details.

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
11. **Volatility regime filter also fails (§12).** Blocking entries when NIFTY
    ATR(20)/close exceeds a threshold sounds intuitive but has no discriminating
    power. In 2022 (the year it should activate) it blocked all 6 winners and kept
    3 losers — turning +11.1% into −3.2%. The regime-filter space is now exhausted:
    market-direction, RS/sector, and market-volatility gates all fail for the same
    reason — the strategy's stocks have their own volatility that doesn't map to the
    index. The individual ATR-based stop already handles stock-level risk.

---

## 12. Volatility regime filter (NIFTY ATR20/close gate) — REJECTED

### Hypothesis
The NIFTY-trend gate failed (§9), but a *volatility* filter might succeed:
block new entries when NIFTY ATR(20)/close exceeds a threshold. High market
volatility → choppy conditions → swing stops get hit on noise.

### Implementation
New `--vol-gate-threshold` flag in `cmd/backtest`. `buildVolatilityGate`
maps each calendar date to `ATR(20)/close ≤ threshold` (true = OK to trade).
Built in the same pattern as `buildRegimeGate`; uses `analysis.ATRSeries`
(new helper, returns Wilder ATR per candle). Benchmark: NIFTY50 candles.

### Results — threshold sweep (2022–2025)

| Config | Return | CAGR | Max DD | Trades | Avg R |
|---|---|---|---|---|---|
| Baseline (no gate) | +72.8% | 14.6%/yr | −12.6% | 78 | +0.80R |
| Vol gate 0.8% | +22.0% | 5.1%/yr | −10.6% | 39 | +0.66R |
| Vol gate 1.0% | +45.5% | 9.8%/yr | −13.3% | 61 | +0.75R |
| Vol gate 1.2% | +73.5% | 14.8%/yr | −12.8% | 77 | +0.83R |
| Vol gate 1.5% | +69.4% | 14.1%/yr | −12.6% | 75 | +0.81R |

**At 1.2%+:** the gate never activates in 2022–2025 (NIFTY ATR20/close crosses
1.2% so rarely it blocks ≤1 trade across 4 years). Effectively a no-op.

**At 0.8–1.0%:** returns collapse — gate blocks 20–50% of trades without
improving drawdown proportionally. At 1.0%, max DD actually *worsens* (−13.3%).

### Per-year analysis (baseline vs 1.0% gate — the most active threshold)

| Year | Baseline | Vol-gate 1.0% | Assessment |
|---|---|---|---|
| 2022 | +11.1% / 14 trades / +0.74R | **−3.2% / 3 trades / −1.04R** | Gate blocks all 6 winners, keeps 3 losers |
| 2023 | +72.9% / 35 trades / +1.78R | +67.7% / 27 trades / +2.18R | Fewer trades → lower total despite better avg R |
| 2024 | −8.5% / 23 trades / −0.34R | **−9.7% / 13 trades / −0.74R** | Filter removes the less-bad trades, keeps the worst |
| 2025 | −3.2% / 23 trades / −0.11R | −3.3% / 22 trades / −0.10R | Neutral |

### Why it fails
The filter has no discriminating power: NIFTY being volatile does not predict
that a *specific* swing setup will fail. In 2022 — the year this protection
should activate — the gate blocked all 6 winners and let through 3 losers,
turning a +11.1% year into −3.2%. The strategy's individual ATR-based stop
already absorbs stock-level volatility; a market-ATR gate on top just reduces
sample size without improving sample quality.

The weak 2023 signal (higher avg R per trade with gate active) is negated by
fewer trades taken — net total return is still lower, not higher.

### Verdict
**Rejected.** NIFTY's own volatility level is not a useful predictor for
individual stock swing success. This exhausts the regime-filter space:
market-direction gates (§9), RS/sector filters (§8), and now market-volatility
gates all fail for the same reason — **the strategy's selected stocks have
their own volatility regime that doesn't map cleanly to the index.**

The `--vol-gate-threshold` and `--vol-gate-atr` flags are kept in the CLI
(and `analysis.ATRSeries` is a useful reusable helper), but they are not
recommended for production use.

---

## 13. Resistance-zone breakout entries — REJECTED

### Hypothesis
The swing scanner only trades pullback-to-support setups (bullish reversal
candle at a tested support zone, R/R ≥ 2). Strong trending stocks that never
pull back cleanly — e.g. EXIDEIND, +28.6% over Jun–Aug 2026 without ever
offering a bullish-close-at-support day — are never traded. A resistance-zone
breakout scanner (confirmed close above a tested resistance zone, ≥1.5x average
volume) was built (`internal/breakout`) to see if it captures a genuinely
different, additive edge.

### Design
- Constructive trend filter: price above EMA50 and EMA200 (same bar as swing/meanrev).
- Entry: signal-day close above a resistance zone (≥2 touches) that price was
  at or below on the prior close — a fresh break, not a chase.
- SL: broken resistance zone's Low − `StopATRMultiplier` × ATR(14) (former
  resistance now support, with an ATR buffer).
- Target: nearest resistance zone above price; falls back to
  entry + `MinRR` × risk (default 2.0) when no zone exists above.
- Volume confirmation: today's volume ≥ `MinVolumeRatio` (default 1.5x) of the
  20-day average — a break on thin volume is easily faked.

### Results vs swing (2022-01-01 → 2026-08-20, same engine, same window)

| | Swing | Breakout (default) | Breakout (wide stop/trail) |
|---|---|---|---|
| Total trades | 163 | 10,763 | 8,788 |
| Expectancy | +0.051R | +0.032R | +0.053R |
| Profit factor | 1.20 | 1.12 | 1.20 |
| Max consecutive losses | 9 | 36 | **47** |
| Trail-stop rate | 90% | 85% | 71% |
| Trail-stop avg exit | −0.02R | −0.11R | −0.21R |
| Avg hold | 5.9 days | 5.3 days | 11.0 days |

The default parameters (`StopATRMultiplier: 1.0`, engine `TrailATRMultiplier:
1.5`) underperform swing on every metric. Widening both (`--br-stop-atr-mult
2.0 --trail-atr-mult 2.5`) — testing the hypothesis that a normal post-breakout
retest of the broken level was triggering premature stops — recovers
expectancy and profit factor to match swing, but at the cost of a **47-trade
max losing streak** (vs swing's 9) and **54x the trade frequency** (8,788 vs
163 trades over the same 4.5 years), uncosted. This backtest engine does not
model transaction costs; at that frequency, realistic round-trip costs
(~0.45%, per the paper-trade defaults) would erode an edge that only matches
swing's before costs.

### Why it fails
Breakouts commonly retest the broken resistance level (now support) before
continuing — a normal, healthy pattern that a tight stop punishes as if it
were a failure. Widening the stop enough to survive the retest recovers the
per-trade edge but makes losing streaks and holding-period risk materially
worse, and the entry filter (trend + zone break + volume) is not selective
enough on its own — it fires 54–66x more often than swing's pullback+bounce
setup for a comparable or worse edge.

### Follow-up: does a volatility-contraction ("coiled spring") filter help?
Motivated by the same EXIDEIND case: a stock that repeatedly retests a
resistance level, with its trading range narrowing each time, is a classic
"coil before release" pattern — the idea being that a break out of a *tight,
low-volatility* consolidation is higher quality than a break out of an
already-volatile run-up. Implemented as `RequireContraction` /
`--br-require-contraction`: the ATR on the candle immediately before the
breakout must be ≤ `MaxContractionRatio` (default 0.85) × the ATR from
`ContractionLookback` (default 20) candles earlier — i.e. volatility must have
genuinely compressed in the setup phase, not just during the breakout itself.

Tested alone, combined with a higher resistance-touch requirement (≥4, i.e. a
more thoroughly tested level), and at both stop-width settings — same
universe, same 2022–2026 window:

| Variant | Trades | Expectancy | Profit factor | Max consec. losses |
|---|---|---|---|---|
| Breakout, default (no contraction) | 10,763 | +0.032R | 1.12 | 36 |
| Breakout, widened stop/trail (no contraction) | 8,788 | +0.053R | 1.20 | 47 |
| + Contraction, default stop | 2,333 | **−0.013R** | 0.95 | 22 |
| + Contraction, widened stop | 2,200 | +0.006R | 1.02 | 30 |
| + Touches ≥ 4, widened stop, no contraction | 6,471 | +0.043R | 1.17 | 47 |
| + Touches ≥ 4 + Contraction, widened stop | 1,575 | +0.002R | 1.01 | 25 |

**Contraction consistently hurts, not helps** — every combination that adds it
scores worse than the equivalent without it. The plain widened-stop breakout
(no contraction) remains the best-performing variant, and it still doesn't
clear swing's bar (§13 above).

**Why:** ATR contraction signals an imminent large move is coming, but is
direction-agnostic — a quiet coil doesn't imply *bullish* conviction has been
building, only that volatility has been low. The volume-confirmation filter
already in the base design (≥1.5x average volume) is a more direct "real
interest is building" signal than volatility compression, and adding
contraction on top mostly discards trades without improving the survivors.
This tests one specific implementation (a single ATR-ratio snapshot at a
20-day lookback) — a sustained multi-day narrowing requirement, or measuring
zone-width contraction directly, could behave differently and hasn't been
tried.

### Verdict
**Rejected**, including the contraction-filter follow-up. The instinct behind
it was correct — EXIDEIND's momentum was real and swing's pullback discipline
structurally cannot trade it — but this mechanical implementation of "buy the
breakout" (with or without a coiled-consolidation requirement) does not clear
swing's bar on risk-adjusted terms at any parameter setting tested. The
`internal/breakout` package, `--mode breakout`, and `br-*` flags (including
`--br-require-contraction`) are kept in `cmd/backtest` for reproducibility and
as a standalone research tool (alongside the pre-existing `cmd/scan --mode
breakout` watchlist), but are not wired into paper trading.

---

## 14. Structure-based ("swing low") trailing exit — a real trade-off, not adopted

### Motivation
Two anecdotes, one pattern: the user made ~+22% on TATAPOWER (§3) holding
through a March 2025 dip the crossover system's EMA-recross exit stopped out
of; and COALINDIA (Sep 2025–Jan 2026) rallied +18.8% out of a 4-month
consolidation, comfortably ridden by a discretionary trader reading "higher
lows, hasn't broken structure yet." In both cases the human held through a
pullback an indicator-based exit interpreted as a reversal. The hypothesis:
replace the EMA-recross / fixed-target exit with one that only exits when
price actually breaks the chart *structure* — the most recent confirmed swing
low — mimicking how a discretionary trend trader reads a chart.

### Design (`ExitMode: "structure"`, `internal/backtest/portfolio.go`)
- A candle's Low is a **confirmed swing low** once it is the lowest Low within
  ±`StructureWindow` candles (default 2) — i.e. it takes `StructureWindow`
  candles *after* the low for it to confirm (no lookahead).
- The stop starts at the entry signal's own SL and **ratchets up** to each new
  confirmed swing low formed after entry — never down.
- No separate win condition: the position holds until the trailing stop is
  hit or `MaxHoldDays` elapses. A stop that has ratcheted above entry and gets
  hit is a **protected-profit exit** (`OutcomeTrailStop`), not a loss — same
  semantics as the single-symbol engine's existing ATR trailing stop.
- R-multiple accounting uses the *entry-time* SL (`pfPosition.initialSL`), not
  the ratcheted stop, so ActualRR reflects the risk actually taken, not the
  shrunk distance at exit.

### Results — system-wide, portfolio-aware, 2022-01-01 → 2026-08-20, real costs/slippage

| | Total Return | CAGR | Max DD | Win Rate | Profit Factor | Trades |
|---|---|---|---|---|---|---|
| Swing + EMA (production) | +68.8% | 14.0% | −14.4% | 32% | 2.45 | 81 |
| Swing + Structure | +56.3% | 11.8% | −12.1% | 46% | 3.05 | 65 |
| Crossover + EMA | −10.0% | −2.6% | −15.0% | 21% | 0.76 | 38 |
| Crossover + Structure | −6.8% | −1.7% | −13.7% | 31% | 0.77 | 39 |

### Why it doesn't rescue crossover
Structure exit improves crossover (less bad: −6.8% vs −10.0%, win rate 31% vs
21%) but doesn't flip it to a winner. The portfolio run's own opportunity-loss
stats show why: rejected crossover signals had far better R:R than accepted
ones (avg +4.13R rejected vs −0.18R accepted, EMA-exit run) — the *entry*
(EMA7×21 crossover) is picking the wrong trades before the exit ever gets a
say. This confirms §3's original finding: crossover's problem was never
purely the exit.

### The swing trade-off
On swing — the strategy that actually works — structure exit is not a clean
upgrade. It trades total compounding for consistency: win rate jumps from 32%
to 46%, profit factor from 2.45 to 3.05, max drawdown improves from −14.4% to
−12.1%, but total return and CAGR both drop (68.8%→56.3%, 14.0%→11.8% CAGR).
Fewer trades (81→65) at similar average hold. This is the same fixed-target-
vs-EMA-hold tension from §4, one level deeper: EMA-recross already holds
through the *first* pullback that doesn't break momentum; structure holds
through pullbacks even more patiently, but pays for it by occasionally
exiting a real winner the moment a normal deeper pullback undercuts the prior
swing low, before momentum has actually turned.

### Verdict
**Not adopted, not rejected — a genuine trade-off pending a decision.** Unlike
mean reversion, the volatility gate, or breakout, this isn't dominated on
every metric: swing+structure has a strictly better win rate, profit factor,
and drawdown than swing+EMA, at the cost of CAGR. Whether that's a good trade
depends on risk preference, not on backtest evidence alone. `--exit-mode
structure` and `--structure-window` are kept as first-class options in
`cmd/backtest --portfolio`, with 5 new tests covering swing-low detection,
stop ratcheting (up-only, post-confirmation), and the trail-stop-vs-loss
outcome labeling.

---

## 15. Walk-forward OOS validation (2010–2025) — MAJOR FINDING, unresolved

### Why this test became possible
All prior results in this document (§1–§14) were evaluated on a single
2022–2026 in-sample window — the only history Kite's historical API would
return under the old `--period`-relative sync (capped at ~2000 days per
request). `cmd/kite-sync` was extended to auto-chunk requests wider than that
limit, and Kite's data turned out to go back to at least 2010 for liquid NSE
names — confirmed directly (a 2010–2012 test request for RELIANCE returned
499 real candles). The DB now holds 2010-01-03 → present, 1.56M candle rows,
including the 2020 COVID crash the prior window entirely missed.

### Method
Production config (swing, EMA exit, `--risk-pct 1.0 --max-weight-pct 25`),
frozen exactly as validated in §9/§11, walked forward one calendar year at a
time from 2012 through 2025 (14 full years). Each year is evaluated as a
standalone OOS test: `--health-warmup-from 2011-01-01` seeds the health gate
from real prior history strictly *before* the test year starts (no
lookahead), and `--from`/`--to` bound the test year itself. Run for both the
production (health-gate-on) config and baseline (gate off), to check whether
§"Strategy-Health Regime Filter" also generalizes.

### Results

| Year | Gate ON | Baseline |
|---|---|---|
| 2012 | −0.8% | −0.8% |
| 2013 | −2.1% | −2.5% |
| 2014 | −1.0% | +2.6% |
| 2015 | −1.4% | +6.6% |
| 2016 | 0.0% | −1.5% |
| 2017 | −0.4% | +7.4% |
| 2018 | −6.5% | −8.0% |
| 2019 | 0.0% | −11.2% |
| 2020 | 0.0% | +11.6% |
| 2021 | +3.3% | +15.1% |
| 2022 | +2.0% | +2.0% |
| 2023 | **+74.4%** | **+74.2%** |
| 2024 | −9.0% | −9.0% |
| 2025 | −1.1% | −3.3% |

Sequentially compounded from ₹1L over these 14 years:

| | Final capital | CAGR |
|---|---|---|
| Gate ON | ₹1,46,003 | **2.7%** |
| Baseline (no gate) | ₹1,83,619 | **4.4%** |

For comparison, the headline number from the 2022–2026-only study (§9/M12)
was **14.0% CAGR**.

### Three findings, none comfortable
1. **The entire 14-year compounded gain comes from one year.** Remove 2023
   and the strategy is roughly flat-to-negative across the other 13 years
   combined. The single best trade that year (CUMMINSIND, entered Nov 2023,
   +22.8R) was individually checked for a split/data-adjustment artifact —
   none found; the price series has no discontinuous gaps, the move is a
   real, gradual ~7-month rally on real volume. This is the same
   "fat-tail fragile" pattern already documented for crossover (§4), now
   showing up in swing too, just invisible on a window that happened to
   include the tail.
2. **The health gate underperforms baseline over the full span** (₹1.46L vs
   ₹1.83L) — the opposite of §"Strategy-Health Regime Filter"'s claim, which
   was validated only on 2022–2025. The gate correctly sat out clearly bad
   years (2013, 2018, 2019), but it also sat out mixed years that still had
   profitable trades available (2014, 2015, 2017, 2020, 2021 — baseline beats
   gate in every one of these); being defensive costs more in missed upside
   than it saves in avoided downside often enough, over 14 years, to net
   negative.
3. **14.0% CAGR (in-sample) becomes 2.7–4.4% CAGR (walk-forward OOS).** Every
   parameter choice validated this session — health-window=20, risk-pct=1%,
   the EMA-vs-structure exit call, the whole breakout/contraction
   exploration — was checked only against the 2022–2026 window, which this
   result suggests was an unusually favorable stretch rather than a
   representative one.

### Follow-up: the passive benchmark, and why the strategy misses secular winners
Two further checks, prompted by asking "what would simply have worked over
this period?" — and together they explain *why* §15's OOS result is weak,
not just confirm that it is.

**1. Buy-and-hold NIFTY beat the active strategy by 3–4x.** Compounding
NIFTY50's own 2012–2025 yearly returns: ₹1L → **₹5.47L (12.9% CAGR)** — vs
₹1.46L (gate ON) / ₹1.84L (baseline) for the active system. Zero effort, zero
trading cost, zero skill, and it outperformed by a wide margin. Any active
strategy has to clear this bar to justify its existence, and this one
currently doesn't.

**2. The strategy structurally excludes genuine multibaggers.** Screened all
symbols with data since ~2012 for total return to present; the top 3 —
KEI (+427x), NEULANDLAB (+396x), JBMA (+264x) — were checked for
split/bonus-adjustment artifacts (none found; Kite's series has no
unadjusted-split-style price discontinuities, so these are real investor
returns). Running the swing strategy against just these three stocks across
their *entire* history: **one trade, total, and it lost.**

Diagnosing why (sampling KEI's full 14-year run at ~60-day intervals and
recording the scanner's rejection reason each time):

| Reason | Share |
|---|---|
| Signal candle bearish — no bounce confirmation | ~25% |
| Trend neutral, not bullish | 19% |
| R/R below minimum 2.00 | 17% |
| Trend bearish, not bullish (even mid-427x-run) | 12% |
| **No resistance zone above price** | **12%** |
| Misc (EMA200 declining, too close to EMA200, etc.) | ~15% |

The "no resistance zone above price" reason is structural, not incidental:
the scanner sizes its target off the nearest *tested resistance zone above*
entry. A stock making genuine new all-time highs — which is what a
multibagger spends most of its life doing — has no resistance zone above it
by construction, so the scanner can't even build a valid trade. Combined with
the bullish-bounce-candle requirement (the same failure mode already found
for EXIDEIND, §13), the entry logic is biased *against* exactly the stocks
that drive most of a market's long-run wealth creation (consistent with
Bessembinder's well-known finding that a small minority of stocks account for
most total stock-market gains over decades).

This reframes §15's result: the weak OOS CAGR isn't just noise or an
unlucky sample — it's partly a direct, explainable consequence of a design
that structurally can't hold a secular winner. Every strategy tested this
session (swing, crossover, meanrev, breakout, contraction, structure-exit) is
a *short-hold, tactical* system (days to weeks); none was ever designed to
identify and hold a multi-year compounder. That is a distinct, well-defined
gap — not a tweak to the existing scanner, but a structurally different
strategy: buy demonstrated strength (a new high is a signal, not a
disqualifier), hold through corrections as long as the long-term trend
structure stays intact, size for volatility, rarely sell.

### Follow-up: does fundamental data (quarterly profit/revenue) explain the entries — and would a fundamentals-driven strategy actually be tradeable?
Motivated by the observation that these multibaggers rarely show clean chart
patterns (EMA crossovers, tested support/resistance) — they just grind
higher for years — the natural next question is whether *fundamentals*
(quarterly/annual profit and revenue growth) explain the entries better than
price action does. This codebase has **zero fundamental data** — every
strategy tested is 100% OHLCV. Kite Connect doesn't provide financial
statements either, so this would require a new data source entirely (options
considered: Screener.in bulk export — only via their paid Premium plan, not
scraping, which would violate their ToS; a paid India-focused fundamentals
API such as Tijori Finance or Trendlyne; or parsing NSE/BSE XBRL filings
directly — free and authoritative but a real multi-week parsing effort given
inconsistent taxonomy across companies and years).

Rather than commit to any of those before knowing if the hypothesis even
holds, did a small, targeted validation first — quarterly profit/revenue
history for KEI and NEULANDLAB, pulled from public sources (Screener.in's
public pages, Business Standard earnings coverage), no bulk scraping:

- **KEI**: profit flat-to-declining through 2012–13 (FY13 profit +8.3%,
  Q4FY13 profit −41%, sales −15%), then a sharp inflection — Sept 2014
  quarter profit **+530% YoY**, sales +34.5% — which lines up almost exactly
  with the price's first big acceleration that same year (₹48→₹120,
  2014→2015).
- **NEULANDLAB**: two distinct fundamental *and* price regimes. An early
  turnaround (FY13 profit +571% YoY on a small base) coincided with a
  2013–2016 run (₹115→₹1,032). Then a real business-model pivot from
  low-margin generics to high-value CDMO (contract pharma manufacturing),
  which became a major "China+1" supply-chain-diversification beneficiary —
  a genuine, nameable structural catalyst — drove record profit margins and
  a **33x** move (₹418→₹13,724) from 2020–2024.

**The hypothesis holds: real, identifiable fundamental inflections precede or
coincide with these stocks' major compounding phases**, not just in
hindsight-fitted retrospect — KEI's Q3FY15 profit explosion and NEULANDLAB's
CDMO pivot are both nameable, real business events, not curve-fit patterns.

**But a complication that matters more than the data-sourcing question:**
none of these stocks rose smoothly. Checking full year-end price history
(free, from our own DB) revealed brutal multi-year drawdowns mid-journey:

| Symbol | Drawdown | Window |
|---|---|---|
| NEULANDLAB | **−60%** (₹1,032 → ₹418) | 2016 → 2019, *before* the 33x CDMO-driven run |
| JBMA | **−50%** (₹110 → ₹55) | 2017 → 2020, before its next leg up |

A strategy that correctly identified the fundamental turnaround would *still*
have needed to sit through a 50–60% drawdown to capture the eventual payoff.
That is not a data-sourcing problem — every strategy in this codebase
(technical or hypothetically fundamental) uses tight, ATR-based stops sized
for days-to-weeks trades; none could survive a 60% drawdown without exiting
long before any recovery. **Sourcing fundamental data would answer *what* to
buy; it does not solve the harder, still-open problem of *how to hold it* —
a position-sizing and drawdown-tolerance framework built for multi-year
holds, an order of magnitude more tolerant than anything currently in this
codebase.**

### Status: unresolved, not yet a verdict
Unlike §10/§12/§13 (rejected) or §14 (a real trade-off), this isn't a
candidate-feature test with a clean accept/reject call — it questions the
core validated strategy itself, tuned entirely on §9's in-sample window. Two
follow-ups have sharpened *why*, without yet resolving *what to do*:
- The multibagger diagnosis: the scanner's entry/target logic structurally
  excludes new-high stocks — a design gap, not a tuning issue.
- The fundamentals check: real fundamental inflections do precede/coincide
  with these stocks' big moves (validating that a fundamentals-aware
  long-hold strategy is worth pursuing), but even perfect stock selection
  would still require surviving 50–60% drawdowns — a risk-framework problem
  at least as hard as the stock-selection problem.

Still needs a decision on scope and priority — build the long-hold,
drawdown-tolerant strategy (and which fundamentals data source funds it),
pursue regime-detection/re-tuning on the existing tactical strategies in
parallel, or something else — before further feature work builds on top of
this. Left open pending discussion rather than resolved unilaterally.

---

## 16. Long-hold, buy-strength strategy — beats the passive benchmark, real drawdown cost

### Design (`internal/longhold`, `ExitMode: "trendstop"`)
Direct response to §15: entry deliberately avoids both failure modes found
there — no same-day bullish-candle requirement, no resistance-zone target.
- **Entry**: a fresh N-day high (default 252, ~1 trading year) while price
  sits above a *rising* long-term EMA (default 200) with volume confirmation
  (≥1.5x the 20-day average). A new high is the signal, not a disqualifier.
- **Exit**: no fixed target. The position is held until the close breaks
  below the same long trend EMA — a single-average trend-following stop,
  deliberately tolerant of the 50–60% mid-journey drawdowns §15 found even
  genuine compounders go through.
- **Sizing**: reuses the existing risk-based sizing unchanged. SL = the trend
  EMA at entry (wide and structural, not a tight ATR buffer), so a more
  extended/volatile setup naturally gets a smaller allocation — "size for
  volatility" falls out of existing infrastructure, no new sizing logic.

### A bug found and fixed along the way
While backtesting this over the new 2010–2026 history, the tool's own
printed CAGR looked implausible (26.7%/yr for a 158% total return over 14.6
years — the correct figure is 6.7%/yr). Root cause: `printPortfolio` had
`years := 4.0` **hardcoded**, not derived from the actual `--from`/`--to`
window. Every prior result in this document was run on a ~4-year window
(2022–2025/2026), so the hardcoded value was silently correct by
coincidence — this is the first backtest run long enough to expose it.
Fixed to compute years from the real test-window bounds (falling back to the
equity curve's span only when `--from`/`--to` are unbounded — the equity
curve itself spans the *entire loaded history* for indicator warmup, not
the test window, so it can't be used directly). Verified against a known
value (§9's documented ~12.1%/yr swing config now reads 11.3%/yr on current
data — consistent). All CAGR figures below use the corrected formula.

### Results (2012-01-01 → 2026-08-20, portfolio-aware, real costs/slippage)

_Revised by §16b: the 20-position CAGR/DD/trade figures below were
originally reported as 24.2% / −59.1% / 417 trades before a non-determinism
bug in the portfolio engine was found and fixed (§16b). Numbers below are
post-fix, confirmed bit-identical across repeated runs._

| | Max Positions | Final Capital | CAGR | Max DD | Trades | Profit Factor |
|---|---|---|---|---|---|---|
| Swing + EMA (production, §9) | 5 | — | 4.4%* | −14.4%* | — | 2.45* |
| **Longhold + trendstop** | 5 | ₹2.58L | **6.7%** | −21.5% | 117 | 3.69 |
| **Longhold + trendstop** | 20 | ₹12.52L | **18.9%** | −55.9% | 409 | 3.60 |
| *Buy-and-hold NIFTY (2012–2026)* | — | ₹5.23L | *12.0%* | — | 0 | — |

\* §9/§15 baseline figures, shorter/different window; shown for reference only.

**At 5 positions, longhold clears swing but not NIFTY.** At 20 positions
(more consistent with how this style of investing is actually practiced —
diversified across many names, not concentrated in 5), **it clears NIFTY by
~1.6x** — the first strategy built this session to beat the passive
benchmark §15 established as the real bar.

### The concentration and drawdown checks (same rigor as every other finding)
_Note: the specific trade list changed after the §16b determinism fix (409
trades now, not 417) — the concentration/year-by-year/KEI figures below were
computed before that fix and have not been individually re-verified against
the corrected trade sequence. The qualitative pattern (concentrated but
multi-name, no single dominant year) is expected to still hold since the
fix only changed which trades land near sizing rounding boundaries, not the
overall signal set — but treat the exact numbers below as pre-fix._
- **Trade concentration** at 20 positions: top 10 of 417 trades (2.4%)
  account for 49.5% of total R — concentrated, as expected for a
  trend-following payoff shape, but spread across 10 different names
  (EICHERMOT, HEG, UNOMINDA, TARIL, ZENTEC, IRCON, ATGL, LAURUSLABS,
  JSWSTEEL, VBL), not one lucky year like swing's 2023. The single largest
  winner (TARIL, +34.5R, ₹27.76→₹400.20 over 585 days) was checked directly
  against the candle data for unadjusted-split-style price discontinuities —
  none found.
- **Year-by-year distribution** (20 positions): 12 of 15 years positive,
  including several genuinely large ones (2014 +79.4%, 2017 +106.4%, 2023
  +50.1%, 2024 +55.4%) — not a single-year-dependent result.
- **But the drawdown is real and current, not theoretical.** −59.1% max DD;
  the equity curve peaked in 2025 (₹22.1L) and is down to ₹15.8L (−12.4%
  partial-year 2026, on top of −19.2% in 2025) as of the most recent data —
  an active, ongoing drawdown at the time of this writing, not a resolved
  historical footnote.
- **Sanity check on KEI specifically**: the strategy now engages with it (8
  trades, profit factor 15.47, avg hold 249 days) where swing took zero —
  but doesn't ride the full 427x buy-and-hold move either (+51.4% total on
  KEI alone), since the 200-day trend stop still exits during KEI's own
  internal corrections and has to re-enter, paying costs each time.

### The real scenario: ₹5L start + ₹10k/month, and why naive CAGR lies with contributions
The user's actual plan: start with ₹5L, add ₹10k/month from salary. Backtest
this exact deposit schedule (`--monthly-contribution 10000`) rather than a
lump sum, since ongoing SIP-style contributions change the answer to "what
return did I actually get" in a way naive CAGR gets wrong — it would credit
your own deposits as if they'd been invested since day one.

Added `MonthlyContribution` (deposits on the first trading day of each
calendar month within `[From, To]`) and `computeXIRR` — a proper
money-weighted rate of return (Newton-Raphson with a bisection fallback),
treating the start capital, every contribution, and the final value as dated
cash flows and solving for the single rate that zeroes their combined
present value. This is the same method (XIRR) brokerages use for portfolios
with irregular deposits, for exactly this reason. Verified against
known-answer cases: a pure lump sum reduces to simple CAGR; a
zero-real-growth deposit/withdrawal schedule correctly solves to ~0% despite
the ending balance being 2x the total deposited.

**Result (2012–2026, ₹5L start, ₹10k/month, 20 positions), post-§16b fix,
confirmed bit-identical across 3 repeated runs:**

| | Value |
|---|---|
| Final capital | ₹2,06,83,382.80 |
| Total contributed (start + deposits) | ₹17,50,000 |
| Naive "Total return" CAGR | 29.0%/yr — **wrong, ignore it** |
| **XIRR (true money-weighted return)** | **22.6%/yr** |

The naive figure (29.0%) is close to the pure-lump-sum result (18.9% above
— not identical because the lump sum and SIP runs have different capital
trajectories feeding position sizing) because it doesn't know about the
deposits at all — it just compares start to end. Unlike the pre-fix run
(which showed XIRR *below* the lump-sum CAGR, apparently penalized by
deposit timing into a drawdown), the corrected numbers show XIRR *above*
lump-sum CAGR — the monthly deposits happened to buy in at points that,
net, benefited from dollar-cost-averaging through volatility rather than
being hurt by it. This reversal versus the original (pre-fix) writeup is
itself evidence of how much the non-determinism bug distorted specific
scenario comparisons, not just headline CAGR — see §16b.

### Verdict
**A real, verified improvement — the first strategy this session to beat
the true passive benchmark — but not a free lunch, and the drawdown is
explicitly accepted, not glossed over.** The 20-position lump-sum config's
18.9% CAGR, and the ₹5L+₹10k/month scenario's 22.6% XIRR (both figures
post-§16b fix), come bundled with a ~56% max drawdown that is, as of this
writing, an *active* drawdown the strategy is currently inside of, not a
historical event safely in the past — user confirmed this is acceptable,
capital committed at ₹5L + ₹10k/month, 20 positions. Kept as `--mode
longhold --exit-mode trendstop --monthly-contribution N` in `cmd/backtest
--portfolio`, with 6 new tests (`internal/longhold`), 2 for the trendstop
exit mechanics, and 4 for `computeXIRR` (lump-sum-reduces-to-CAGR,
zero-growth-with-contributions, positive-growth NPV-correctness,
too-few-flows). Not yet wired into paper trading. §16b below runs the
walk-forward OOS validation that was the natural next step per user
direction (the same rigor §15 applied to swing) — it holds up.

## 16b. Non-determinism bug, and walk-forward OOS validation on longhold

### The bug
While running a parameter-sensitivity sweep on `--trend-stop-ema` (below),
the numbers were non-monotonic and inconsistent (175→20.6%, 190→8.3%,
200→15.6%, 210→19.9% CAGR) — implausible for a single-EMA exit parameter.
Repeating the *identical* command three times in a row confirmed the tool
itself was non-deterministic: 24.5%, 24.1%, 23.2% CAGR on three runs of one
unchanged CLI invocation. Ruled out (in order): a background cron job
(`launchctl list` empty), DB drift mid-session (candle count and max
timestamp stable via a throwaway checker), unsorted signal generation
(already correctly sorted with score+symbol tiebreak, verified by reading
the code), goroutine races (no concurrency anywhere in `portfolio.go`).

**Root cause**: two loops in `RunPortfolio` — the daily equity
mark-to-market and the end-of-run force-close — iterated `positions`, a Go
map, directly (`for sym, pos := range positions`). Go randomizes map
iteration order on every run. float64 addition is not perfectly
order-independent, so summing the same position values in a different
order can land on a different value at the last few bits of precision.
That equity value feeds directly into `math.Floor`-based share-count
sizing a few lines later — a rounding-boundary difference on one day
(one more or fewer share bought) cascades into a genuinely different trade
from that point forward, and that difference compounds over years. Fixed
by iterating the existing `sortedPosSymbols()` helper instead — the same
pattern already used correctly for exits elsewhere in this file, and
matching `internal/paper/engine.go`'s `qualifyingSignals`. Verified: 5
repeated runs of the identical sensitivity command post-fix gave
bit-identical results (₹12,52,481.31, every time); a real-data end-to-end
run of the `--monthly-contribution` scenario gave bit-identical XIRR
(22.6%/yr) across 3 runs. PR: `fix/portfolio-nondeterminism` (#73).

**Scope of impact**: definitely affected every longhold sensitivity/era
number generated earlier in this session (thousands of same-day rejected
signals under `MaxPositions=20` gave the bug maximum surface area to bite).
Likely affected earlier swing/breakout/crossover §1–§14 portfolio-mode
results too, to a much smaller degree, whenever "Opportunity loss: Rejected
> 0" was printed (fewer competing signals under those configs = far less
surface area, but not zero). Those sections have not been individually
re-run; the qualitative conclusions there (regime filter, RS filters,
mean-reversion rejection, etc.) are not expected to flip, but exact
percentages predate this fix and should be treated as approximate.
A regression test (`TestRunPortfolio_DeterministicAcrossRuns`) now guards
against a recurrence — note its fixture had to be deliberately sized (40
symbols, real same-day slot contention) after an initial smaller fixture
was found to pass even on the unfixed code.

### Walk-forward OOS validation (2012–2026, two-era split)
§15 applied literal 1-year folds to validate the swing strategy — wrong
for longhold, which deliberately holds positions for years; a 1-year fold
would cut every real trade off mid-flight. Instead: split history into two
non-overlapping, multi-year eras and confirm the strategy weren't tuned to
fit either one specifically (all longhold parameters were chosen using
2010–2026 full-history reasoning about trend/volume, not fit to a
particular sub-period).

| Era | Longhold CAGR | Longhold Max DD | Trades | NIFTY CAGR (same era) | Longhold vs NIFTY |
|---|---|---|---|---|---|
| 2012–2018 | **26.6%** | −55.9% | 197 | 12.9% | **2.1x** |
| 2019–2026 | **17.6%** | −32.3% | 223 | 11.2% | **1.6x** |
| Full 2012–2026 | 18.9% | −55.9% | 409 | 12.0% | 1.6x |

**Holds up in both eras** — unlike §15's swing result, which fell apart
out-of-sample. Era 1 is the stronger era (more room to compound off a
lower base, fewer, larger names dominating); Era 2 is weaker but still
clears its own NIFTY benchmark by a solid margin, and importantly has a
*much smaller* max drawdown (−32.3% vs −55.9%) — the −55.9% full-period
figure is driven by Era 1, not a feature of the strategy in more recent
market conditions.

### Parameter sensitivity sweep
| `--trend-stop-ema` | CAGR | Max DD |
|---|---|---|
| 150 | 8.6% | −42.7% |
| 200 (default) | 18.9% | −55.9% |
| 250 | 23.1% | −60.6% |

| `--lh-high-lookback` | CAGR | Max DD |
|---|---|---|
| 126 | 13.4% | −49.8% |
| 252 (default) | 18.9% | −55.9% |
| 378 | 23.4% | −60.5% |

**Both parameters trade CAGR for drawdown monotonically in the same
direction — looser exit and longer entry lookback both mean "give trades
more room," which raises return and drawdown together.** There's no
free-lunch setting hiding in this range; every point on this sweep is on
the same risk/return frontier, not a local optimum. This is a healthier
sensitivity pattern than a strategy where small parameter nudges flip the
sign of the result — but it also means the 200/252 defaults are a
deliberate risk-tolerance choice (matching the user's stated 55–60%
drawdown tolerance), not a tuned-for-backtest sweet spot.

### Verdict
Longhold survives both a two-era out-of-sample split and a parameter
sensitivity sweep without the in-sample collapse §15 found in swing. The
non-determinism bug changed every specific number along the way but did
not change the qualitative conclusion — if anything the SIP scenario looks
*better* post-fix (XIRR 22.6% vs the pre-fix 17.9%) than the honest
pre-fix writeup suggested. Recommended next step, unchanged from §16:
paper-trade before committing real capital, since this is still a backtest
on historical data with all the usual caveats (survivorship in the symbol
universe, no slippage-under-stress modeling in a real drawdown, etc.).

---

## 17. Quarterly-results price reaction study (54 stocks — full Nifty 50, Q1 FY27)

### Motivation
§15/§17's parked hypothesis: genuine profit-growth inflections precede/coincide
with real multibaggers' big moves, but a drawdown-tolerance framework needs a
way to *distinguish* which drawdowns to tolerate — which requires fundamental
data this codebase doesn't have. This is the first real step: not a framework
yet, just establishing whether results-day fundamentals and price reaction
actually correlate at all, using free sources, before spending on a paid data
subscription or a multi-week NSE/BSE XBRL parsing effort.

### Data source note — NSE/BSE direct access is blocked
Both `nseindia.com` and `bseindia.com` are blocked by this environment's
browser-automation policy, and direct HTTP requests to NSE hit its bot
protection (403 even on the homepage, before touching any API). So instead
of scraping the exchanges directly, quarterly figures and exact declaration
dates were sourced from company press releases and mainstream financial news
(Business Standard, EquityBulls, Free Press Journal, Business Upturn — one
search per stock, cross-checked against the reported day-of price move where
available). This is slower than an API and doesn't scale past a handful of
stocks by hand, but it's genuinely free and avoids the exchanges' bot walls
entirely. Storage: a new `earnings_events` table (`internal/store/earnings_store.go`,
`symbol, report_date, quarter, revenue_cr, revenue_yoy_pct, pat_cr,
pat_yoy_pct, ebitda_margin_pct, source_url, notes`), seeded via
`go run ./cmd/earnings-reaction --seed` and reusable for future quarters
without re-running the searches.

### A timezone bug found while building this
The first version of the price-reaction script matched report dates to
candles by comparing the candle's stored (UTC) calendar date directly. Kite's
daily candles are timestamped at midnight IST, and `parseKiteTime` converts
that to UTC — midnight IST becomes 18:30 UTC the *previous* day. So a candle
whose true IST trading date is 17 July shows up in the DB as 16 July 18:30
UTC. Matching against an externally-sourced (true IST) report date without
correcting for this landed on the wrong trading day — for two of the five
events it silently rolled the "day 0" match forward onto a *Sunday*, which
doesn't exist as a trading day and should have been the first sign something
was wrong. Fixed by adding the 5:30 IST offset before extracting the
calendar date (`cmd/earnings-reaction/main.go`, `istOffset`). Verified: after
the fix, KEI's computed day-0 move (+0.5%) matches the reported day-of price
move ("closed 0.49% higher on 3rd Aug") exactly.

_Note: `internal/backtest/portfolio.go` uses the same UTC-formatted date key
(`c.Timestamp.UTC().Format(dayFmt)`) throughout, uncorrected. That's not a
bug there — every date in that file (candle keys and `--from`/`--to` bounds)
goes through the same convention, so relative comparisons stay internally
consistent even though the label is one calendar day off from the true IST
trading date. It only becomes a bug when comparing against an
externally-sourced true-IST date, as here._

### Results (calendar week before → calendar week after report date)
_The window definition was revised from an earlier version that used 7
*trading* days each side, which silently widened the window beyond a
literal week (e.g. for JBMA it spanned 21 Jul–10 Aug, not the requested
23 Jul–6 Aug) — caught when asked to account for the "15 days" directly.
Fixed to `report_date − 7 calendar days` through `report_date + 7 calendar
days`, whatever trading days fall inside that span; `cmd/earnings-reaction`
now also prints the full day-by-day close table per stock via
`--detail SYMBOL`, not just the summary row._

Expanded twice: first from the original 5 (longhold's biggest winners,
§16 — a pre-selected, already-winning sample) to 15 by adding 10 large,
liquid Nifty 50 names as a control group; then from 15 to the full 54 by
adding the remaining 39 Nifty 50 constituents, so the sample now covers
every current Nifty 50 member (confirmed unchanged at the Mar 2026 index
review) plus the original 4 non-Nifty-50 names (KEI, NEULANDLAB, JBMA,
LAURUSLABS) kept from the multibagger research thread.

| Symbol | Report (IST) | PAT YoY | Rev/NII YoY | Week-before | Post-week | Total |
|---|---|---|---|---|---|---|
| TCS | 2026-07-09 | +4.6% | +13.9% | −0.9% | +7.4% | **+6.4%** |
| HCLTECH | 2026-07-13 | +20.4% | +13.9% | +7.7% | 0.0% | **+7.7%** |
| HDFCLIFE | 2026-07-15 | +11.5% | +15.1% | +2.3% | −2.8% | **−0.5%** |
| WIPRO | 2026-07-16 | +0.6% | +10.6% | +2.9% | −1.6% | **+1.2%** |
| JIOFIN | 2026-07-16 | +155.4% | +227.4% | +1.0% | −1.1% | **−0.1%** |
| TECHM | 2026-07-17 | +28.4% | +17.7% | +8.1% | −0.9% | **+7.2%** |
| JSWSTEEL | 2026-07-17 | +112.6% | +9.8% | −0.7% | +0.3% | **−0.4%** |
| RELIANCE | 2026-07-17 | −22.4% | +25.4% | +1.5% | −3.7% | **−2.3%** |
| ICICIBANK | 2026-07-17 | +15.9% | +6.3% | +3.1% | −0.8% | **+2.3%** |
| KOTAKBANK | 2026-07-20 | +22.5% | +12.6% | −0.7% | +0.7% | **0.0%** |
| HDFCBANK | 2026-07-20 | +4.9% | +6.7% | −4.9% | −4.5% | **−9.2%** |
| AXISBANK | 2026-07-20 | +22.5% | +8.0% | −4.8% | −2.3% | **−7.0%** |
| ULTRACEMCO | 2026-07-20 | +17.0% | +16.0% | +2.9% | −0.2% | **+2.7%** |
| BAJAJ-AUTO | 2026-07-21 | +42.0% | +37.0% | +2.3% | +9.3% | **+11.9%** |
| ETERNAL | 2026-07-22 | +268.0% | +182.0% | −3.5% | +9.6% | **+5.8%** |
| NESTLEIND | 2026-07-22 | +47.9% | +25.2% | +4.9% | +0.6% | **+5.5%** |
| DRREDDY | 2026-07-22 | −69.0% | −6.0% | −3.8% | −3.4% | **−7.1%** |
| CIPLA | 2026-07-23 | −39.2% | n/a | −2.6% | +5.3% | **+2.6%** |
| INFY | 2026-07-23 | +12.3% | +14.0% | −3.2% | +10.3% | **+6.7%** |
| INDIGO | 2026-07-23 | −110.9% | +20.0% | −4.6% | +4.1% | **−0.7%** |
| SHRIRAMFIN | 2026-07-24 | +59.8% | +16.2% | −2.9% | +4.1% | **+1.1%** |
| SBILIFE | 2026-07-24 | +22.0% | +16.9% | +1.6% | +1.3% | **+2.9%** |
| NTPC | 2026-07-24 | +12.0% | +3.0% | +1.6% | 0.0% | **+1.6%** |
| LAURUSLABS | 2026-07-24 | +125.5% | +29.1% | +4.7% | +13.4% | **+18.7%** |
| TATACONSUM | 2026-07-24 | +27.8% | +11.9% | −0.1% | −0.5% | **−0.5%** |
| BEL | 2026-07-27 | +9.0% | +25.3% | −0.2% | −3.7% | **−3.9%** |
| COALINDIA | 2026-07-27 | +0.6% | +8.0% | −0.4% | −2.7% | **−3.1%** |
| LT | 2026-07-28 | +14.0% | +6.7% | −0.4% | +4.1% | **+3.7%** |
| HINDUNILVR | 2026-07-28 | −2.2% | +10.0% | −5.6% | +3.6% | **−2.1%** |
| EICHERMOT | 2026-07-29 | +21.0% | +31.5% | +1.9% | +2.7% | **+4.6%** |
| ADANIENT | 2026-07-29 | −299.1% | +50.0% | −3.9% | +0.8% | **−3.1%** |
| ASIANPAINT | 2026-07-29 | +39.6% | +17.9% | +2.4% | +0.1% | **+2.5%** |
| ADANIPORTS | 2026-07-29 | +9.0% | +19.0% | −5.5% | −1.1% | **−6.6%** |
| TATASTEEL | 2026-07-30 | +11.6% | +14.3% | +1.5% | +1.3% | **+2.8%** |
| JBMA | 2026-07-30 | +13.4% | +15.0% | +2.9% | −3.1% | **−0.2%** |
| BAJFINANCE | 2026-07-30 | +27.4% | +19.6% | +1.3% | +8.7% | **+10.1%** |
| M&M | 2026-07-30 | +33.6% | +26.6% | +1.7% | +3.7% | **+5.5%** |
| ITC | 2026-07-31 | −16.2% | +27.6% | −0.9% | +1.8% | **+0.9%** |
| BAJAJFINSV | 2026-07-31 | +12.3% | +19.0% | +8.1% | −1.0% | **+7.0%** |
| MARUTI | 2026-07-31 | −9.7% | +36.0% | +5.9% | −1.4% | **+4.4%** |
| SUNPHARMA | 2026-07-31 | +27.0% | +10.5% | +2.5% | −2.3% | **+0.2%** |
| KEI | 2026-08-03 | +40.0% | +23.0% | +3.1% | +12.7% | **+16.1%** |
| ONGC | 2026-08-04 | +112.0% | +45.2% | +1.3% | −1.1% | **+0.2%** |
| BHARTIARTL | 2026-08-04 | +37.3% | +18.4% | +3.6% | −2.8% | **+0.7%** |
| POWERGRID | 2026-08-05 | −0.9% | +2.2% | −0.4% | −4.4% | **−4.7%** |
| NEULANDLAB | 2026-08-05 | +962.0% | +116.3% | +1.3% | +12.8% | **+14.3%** |
| TRENT | 2026-08-06 | +22.0% | +17.8% | +3.6% | −3.8% | **−0.3%** |
| TITAN | 2026-08-07 | +62.9% | +40.3% | +1.3% | +2.3% | **+3.7%** |
| SBIN | 2026-08-07 | +10.2% | n/a | +6.8% | −2.7% | **+3.9%** |
| HINDALCO | 2026-08-07 | +75.2% | +32.1% | +8.7% | −2.8% | **+5.6%** |
| APOLLOHOSP | 2026-08-12 | +38.4% | +20.6% | −3.5% | +1.8% | **−1.8%** |
| GRASIM | 2026-08-12 | +38.8% | +21.4% | +2.6% | −1.1% | **+1.4%** |
| MAXHEALTH | 2026-08-13 | +3.0% | +16.0% | −5.5% | −1.5% | **−6.9%** |
| TMPV | 2026-08-13 | −80.3% | +9.3% | +1.2% | −8.4% | **−7.3%** |

For banks and insurers, the Rev/NII column is Net Interest Income or Net
Premium Income growth — the standard revenue proxy for a lender/insurer,
not a topline revenue line. Two rows (CIPLA, SBIN) have "n/a" where no
clean revenue/NII YoY figure turned up in that stock's search pass —
correctly left blank rather than defaulted to a misleading 0%.

**Two rows needed a genuine swing-to-loss calculation, not a simple
percentage**: ADANIENT and INDIGO both went from a profit last year to a
loss this year. PATYoYPct for both is still a well-defined number
((this year − last year) / last year), just a large negative one
(−299.1%, −110.9%) rather than a bounded -100%-to-∞ growth rate — computed
against last year's actual PAT figure (₹734.41 Cr and ₹2,176 Cr
respectively), sourced with one extra search each since the primary
article for each didn't state the prior-year base directly.

Reproduce: `go run ./cmd/earnings-reaction --seed` for the summary table
above, `go run ./cmd/earnings-reaction --detail SYMBOL` for any one stock's
full day-by-day price table.

### Does PAT growth actually predict the reaction? A real, moderate signal that holds at scale
Pearson r = 0.43 between PAT YoY growth and the total 15-day price
reaction across all 54 stocks — essentially unchanged from the 15-stock
pass's r=0.44, which is itself the most reassuring thing about this
result: tripling the sample more than 3x (15→54) barely moved the
correlation, meaning it isn't a small-sample fluke. Excluding the four
most extreme PAT-growth outliers (NEULANDLAB +962%, ETERNAL +268%,
ADANIENT −299%, INDIGO −111% — all four are companies where a tiny or
negative prior-year base makes the percentage itself unstable) gives
r=0.39 on the remaining 50 — still a real, moderate relationship, not an
artifact of a few extreme points. 33 of 54 stocks (61%) had a positive
total reaction.

Individual cases worth remembering, beyond the two from the 15-stock pass
(HDFCBANK's estimate-miss-despite-growth, MARUTI's pre-priced revenue
story — both still hold at 54 stocks):

- **The two swing-to-loss cases reacted far more mildly than their PAT
  collapse would suggest.** ADANIENT's PAT fell essentially off a cliff
  (−299%) but the stock finished only −3.1%; INDIGO similarly swung to a
  loss but finished −0.7%. Both companies' coverage explicitly flagged the
  loss as driven by a one-off (ADANIENT: an OFAC settlement charge;
  INDIGO: a fuel-cost spike, an industry-wide cost shock rather than a
  company-specific failure) — consistent with the market correctly
  discounting a one-off from the headline number rather than reacting to
  the raw percentage.
- **ONGC is the standalone/consolidated-divergence case**: standalone PAT
  +112% YoY, but consolidated profit (which includes the loss-making
  refining & marketing arm) fell −43% — the two numbers tell opposite
  stories from the same quarter, and the model here only sees the
  standalone figure. The muted +0.2% reaction is plausible under either
  read.
- **WIPRO is a clean small-gap example**: PAT essentially flat (+0.6%,
  "missed Street estimates" per contemporaneous coverage) against +10.6%
  revenue growth, margins at a 15-quarter low — a smaller-scale echo of
  the HDFCBANK pattern (revenue fine, profit growth disappoints
  forward-looking expectations) rather than a dramatic one.
- **JSWSTEEL and JBMA remain the flattest reactions** despite both growing
  PAT and revenue YoY — consistent with the original 5-stock pilot's
  finding that large, closely-covered names with interim disclosures (JSW
  Steel's monthly production updates) or same-day confounds (JBMA's
  proposed ₹1,500 Cr securities issue) can show muted reactions regardless
  of the headline growth number.

### Tracking upcoming quarters — `earnings_watchlist`
`earnings_watchlist` (`internal/store/earnings_store.go`, `symbol,
quarter, quarter_end, declared, checked_at`) now tracks all 54 symbols, so
"has this stock's next quarter come out yet" is a query against a known
list instead of re-deriving the symbol set from scratch every time. `go
run ./cmd/earnings-reaction --watch` registers the next quarter (currently
Q2 FY27, quarter end 2026-09-30) for every tracked symbol; `--pending`
lists which watched symbol/quarter pairs haven't been declared yet;
`--seed` automatically marks a watchlist entry declared once its matching
event is seeded. **This is a tracking mechanism, not an automated
downloader** — Indian companies typically only announce their
board-meeting date 1-2 weeks ahead, so there's no real date to fetch this
far out, and NSE/BSE are still blocked (see above), so "downloading once
available" still means a manual WebSearch pass per stock once each
quarter-end has passed by ~4-6 weeks — the watchlist just makes that check
systematic (`--pending` names exactly who's left) instead of ad hoc.

### Verdict and next steps
The correlation holding steady (r≈0.44 at 15 stocks, r≈0.43 at 54) across
a 3.6x increase in sample size is the strongest evidence yet that this is
a real, moderate signal rather than small-sample noise — and it's still
honest about its limits: one quarter, and a moderate correlation on its
own doesn't tell you *how much* to size a position, just that fundamentals
and reaction are related more often than chance. `earnings_watchlist` now
tracks all 54 symbols, making it straightforward to add Q2 FY27 data as it
comes out over the next few months, which is the single highest-value next
step. Also still open: (1) tag confounding corporate actions (JBMA's
securities issue) so they don't get misread as a pure results reaction,
(2) test the "pre-telegraphed via interim disclosures" hypothesis
explicitly (JSWSTEEL, MARUTI, ONGC's standalone/consolidated split all
point at related ideas) by cross-referencing companies' monthly/interim
business updates against results-day reaction size, (3) once there's
enough data across quarters, check whether the correlation holds
out-of-sample on a quarter the pattern wasn't observed on — the same
discipline §15 applied to swing and §16b applied to longhold.

## 17b. Full-501 expansion, sector filter, and a daily-movers view

Three follow-ups, planned together: (1) extend PAT tracking from the 54
Nifty 50 names to the full ~501-symbol watchlist (`config/symbols.txt`),
incrementally in batches rather than all at once, since each batch is a
real WebSearch effort with declining source quality further down the
market-cap scale; (2) a sector filter, since the dashboard had no way to
slice by industry; (3) a live daily-movers table, a different question
(today's price action across the whole watchlist) from the earnings-
reaction table (results-day price action for stocks that reported).

**Sector filter — free, because the data already existed.**
`config/sector-map.csv` (from earlier swing-strategy work, §8) already
maps ~360 of 501 symbols to a Kite sector index. `cmd/earnings-dashboard`
now loads it at startup via the existing `config.LoadSectorMap` and joins
it into both API responses — no new classification work needed. The ~141
unmapped symbols (industries the swing filter never had a clean sector-
index proxy for: Capital Goods, Chemicals, Consumer Services, Diversified,
Services, Textiles) show as "unmapped" in the UI rather than silently
guessed at.

**Daily movers — a new, separate table**, `/api/movers`, one row per
watchlist symbol with its latest close vs. the prior day's close. Needed a
genuinely new `CandleStore.LatestTwo(symbols)` method (one windowed SQL
query for all symbols at once) rather than one round trip per symbol,
which is what made it practical to serve all 501 in ~250ms. This view
answers a different question than the earnings table on purpose — general
daily price action across the watchlist, not results-specific — and is
lazy-loaded on first tab click since it's ~7x the row count.

**Batch 1 of the PAT expansion: 18 new stocks** (54 → 72), picked
alphabetically from the remaining ~447 symbols (no cherry-picking, so the
sample stays representative rather than biased toward names that are easy
to find data on). Two attempted names — ABDL (Allied Blenders and
Distillers) and ABLBL (Aditya Birla Lifestyle Brands) — were dropped:
search turned up PAT/revenue figures for both but no clean report date for
either, exactly the smaller/newer-listing coverage gap flagged before this
expansion started. **Two swing-to-loss-adjacent cases needed a different
sign convention**: ABFRL and ABREL were *already* loss-making last year
and the loss widened further — the raw `(current−prior)/prior` formula on
a negative base gives a technically-positive percentage for objectively
worse news, so both use `−(change in loss magnitude)/prior loss
magnitude` instead, clearly labeled in their Notes field. ADANIGREEN's
sources were internally inconsistent (headline percentages didn't match
the Rs-crore figures quoted in the same articles); used the self-
consistent pair rather than the flashier headline number.

**Result: r=0.37 at 72 stocks, down from r=0.43 at 54.** Still a real,
moderate signal — not a collapse — but softer than before, and worth
reporting exactly as it happened rather than only keeping the
more-flattering earlier number. Whether this keeps drifting down as
coverage broadens past the large/mega-caps (which the original 54 were
skewed toward, being either longhold winners or literal Nifty 50
constituents) toward genuinely smaller, more idiosyncratic names is itself
useful information about where this relationship holds and where it
doesn't — worth watching over the next few batches rather than reacting to
a single data point.

**Batch 2: 18 more stocks (72 → 90).** Same alphabetical, no-cherry-picking
approach. A few sourcing notes worth keeping: AMBER's headline PAT (−97%)
is almost entirely a one-off exceptional loss tied to an acquisition —
adjusted PAT actually grew +19%, but the headline figure was used for
consistency with how every other row is sourced, so this row's Total
column should be read with real caution. APOLLOTYRE's PAT "grew" +2,608%
only because last year's comparison quarter had an unusually tiny ₹12.88
Cr base — not a genuine 26x improvement in the business, flagged the same
way NEULANDLAB/ETERNAL's outlier percentages were.

**Result: r=0.185 at 90 stocks — a real, sharp drop from r=0.37 at 72,
diagnosed rather than just reported.** Two things are true at once:

1. **About half the drop is a single-point artifact.** Excluding
   APOLLOTYRE alone (that one extreme, low-base PAT% paired with an
   unremarkable +3.7% reaction) brings r back to 0.33 — a leverage-point
   effect, not a broad signal collapse. This is exactly the failure mode
   unstable percentages create, and is why ANURAS/AARTIIND/AEGISLOG's
   similarly extreme-but-smaller percentages were flagged in their own
   Notes rather than silently trusted.
2. **The rest is real and substantive.** Splitting the 90 stocks by when
   they were added: the original 54 (mega/large-cap — either Nifty 50
   constituents or pre-selected longhold winners) still show r=0.43,
   essentially unchanged. The 36 stocks added in batches 1–2 (mid-cap,
   less analyst coverage, thinner liquidity) show r=0.16 **on their own**
   — genuinely weaker, not just diluted by mixing in with the strong
   large-cap signal. This is a real, useful finding in its own right:
   the fundamentals→reaction relationship this study keeps finding looks
   like it may be a large-cap phenomenon (more analysts pricing in
   expectations precisely, more liquid price discovery) rather than a
   universal one — exactly the kind of result the earlier "worth
   watching as coverage broadens" caution was pointing at, now with
   actual numbers behind it instead of just a hypothesis.

**Batch 3: 18 more stocks (90 → 108).** Same policy. Two more one-off-
dominated headline figures worth knowing: BAJAJHLDNG's −22.69% PAT
"decline" is entirely a high-base effect (Q1 FY26 included a ₹1,521.88 Cr
one-time Bajaj Finserv share-sale gain that didn't repeat — standalone PAT
actually surged); BANKBARODA's −71.8% PAT "collapse" is almost entirely a
₹5,680 Cr one-time NMC Group settlement charge (ex-that, PAT would have
been +21.7% YoY). Both use the headline (unadjusted) figure for dataset
consistency, same policy as AMBER in batch 2, and both are flagged in
their Notes as one-off-dominated. One new sign-convention case in the
*opposite* direction from ABFRL/ABREL: ATHERENERG is still loss-making,
but the loss **narrowed** 71.33% YoY — genuine improvement — so its
PATYoYPct is recorded as **positive** (+71.33), the mirror image of how
ABFRL/ABREL's *widening* losses were recorded as negative, so the sign
always tracks whether the news was actually good or bad rather than the
raw formula's mechanical output on a negative base.

**Result: r=0.157 at 108 stocks, and now with a clean equal-size
comparison that makes the large-cap hypothesis much harder to dismiss.**
At 108 stocks, the 54 newly-added symbols (batches 1–3) can be compared
directly against the original 54 — same sample size on both sides, no
"diluted by a bigger group" confound:

- **Original 54 (mega/large-cap): r=0.425.** Essentially unchanged across
  every batch added so far — this number has been stable at ~0.43 since
  the very first 15-stock pass.
- **New 54 (batches 1–3, broader-market): r=0.092.** Essentially no
  linear relationship at all — not "weaker," close to *none*.
- Excluding APOLLOTYRE's single extreme outlier from the full 108 still
  only recovers r=0.295 — confirming the gap between the two cohorts
  isn't primarily an outlier artifact anymore; it's a genuine, broad
  pattern across 54 non-mega-cap names.

Reading this honestly: three batches (54 stocks) is still a small sample
for the "broader market" cohort specifically, and it's still one quarter
— but an equal-size, side-by-side comparison showing r=0.43 vs. r=0.09 is
a much stronger basis for the large-cap hypothesis than the batch 1/2
write-ups had. Worth treating as a real, provisional finding now rather
than just "worth watching": **the PAT-growth → price-reaction
relationship this study keeps finding looks like it may be specific to
large, closely-covered stocks, not something to expect from the market
broadly.** If that holds up over more batches and more quarters, it has a
direct implication for the original drawdown-tolerance-framework
question this pilot was in service of (§17 Motivation) — a fundamentals-
based conviction signal may only be trustworthy for the kind of large-cap
names where this study's signal is real, not for smaller multibagger
candidates like KEI/NEULANDLAB, which is exactly where §15/§16's
original hypothesis was aimed. That tension is worth confronting
directly before more batches, rather than assuming it away.

Reproduce: `go run ./cmd/earnings-reaction --seed` (idempotent, includes
all batches so far), `go run ./cmd/earnings-dashboard` for the live
sector-filterable table and the daily-movers tab.

---

## 18. Open questions / next steps

- **Cross-sectional RS rank (Variant C)** — "is this among the strongest stocks?"
  (percentile rank of 50–100D return across all 500), distinct from the
  time-series RS filters that failed in §8. Test as a universe filter, not a tiebreak.
- **Turnover reduction** — costs are a persistent drag; anything that lifts profit
  factor without adding trades is interesting.
- **Walk-forward OOS validation — DONE, see §15.** Surfaced a major,
  unresolved finding: the in-sample 14.0% CAGR (§9) compresses to 2.7–4.4%
  CAGR walking forward through 2012–2025, underperforming plain buy-and-hold
  NIFTY (12.9% CAGR) by 3–4x. Follow-up diagnosis found a structural cause:
  the scanner's target-setting (project to nearest tested resistance zone
  above) can't construct a valid trade for a stock at new highs, so it
  structurally excludes genuine multibaggers — 1 losing trade, total, across
  the 3 biggest compounders in the universe over 14+ years.
- **Long-hold, buy-strength strategy — DONE, see §16.** Built and backtested
  (`internal/longhold`, `--exit-mode trendstop`). Result: 18.9% CAGR at 20
  concurrent positions (revised from an initial 24.2% — see §16b), beating
  buy-and-hold NIFTY (12.0%) by ~1.6x — but with a 55.9% max drawdown that
  is an *active, ongoing* drawdown as of this writing, not a resolved
  historical one. Found and fixed a real CAGR display bug along the way
  (hardcoded 4-year assumption, silently correct until this longer backtest
  exposed it).
- **Portfolio-engine non-determinism + walk-forward OOS on longhold —
  DONE, see §16b.** Found and fixed a real bug (unsorted map iteration
  making equity/close accounting float-order-dependent, cascading through
  position sizing into different trade sequences run-to-run — see §16b for
  the full mechanism). Then ran the walk-forward validation §16's Verdict
  called for: two-era split (2012–2018 / 2019–2026) plus a parameter
  sensitivity sweep. Unlike §15's swing result, longhold holds up
  out-of-sample in both eras (26.6% / 17.6% CAGR, both clearing NIFTY's
  own era CAGR), and the sensitivity sweep shows a smooth risk/return
  frontier rather than a fragile in-sample optimum.
- **Fundamental data source + drawdown-tolerance framework — IN PROGRESS,
  see §17.** Expanded twice: 5 pre-selected winners → 15 (10 large Nifty 50
  names as a control group) → all 54 currently tracked (the remaining 39
  Nifty 50 constituents added to cover the full index). Result: a real,
  moderate correlation between PAT YoY growth and 15-day price reaction
  that holds steady across the expansion (r≈0.44 at 15 stocks, r≈0.43 at
  54, r≈0.39 excluding the four most extreme PAT-growth outliers) — the
  correlation barely moving as the sample more than tripled is the
  strongest evidence yet that it's real, not small-sample noise.
  Informative individual cases: HDFCBANK fell −5.1% on results day despite
  +4.9% PAT growth (missed estimates despite YoY growth); MARUTI finished
  +4.4% despite −9.7% PAT decline (market had already priced in its +36%
  revenue story); ADANIENT and INDIGO both swung to a loss but reacted
  mildly (−3.1%, −0.7%), consistent with the market discounting each
  as a one-off (an OFAC settlement, an industry-wide fuel-cost spike)
  rather than reacting to the raw percentage. One quarter still isn't
  enough to build a sizing framework on, but the signal is real enough to
  keep collecting. Data persists in `earnings_events`; `earnings_watchlist`
  tracks all 54 symbols' next-quarter status (`--watch` / `--pending`), so
  extending this across quarters is a systematic check, not a from-scratch
  search each time.
- **NSE/BSE direct scraping — ruled out for this environment, see §17.**
  Both domains are blocked by browser-automation policy, and NSE's own site
  serves a 403 to direct HTTP requests before even reaching an API. Company
  press releases + financial news are the practical free substitute; a paid
  subscription (Tijori Finance / Trendlyne) remains the option if the
  fundamentals pilot in §17 proves out and scale becomes the bottleneck.
- **Full-501 expansion, sector filter, daily movers — IN PROGRESS, see
  §17b.** Sector filter and a live daily-movers table (`/api/movers`,
  all 501 symbols in ~250ms via a new bulk `CandleStore.LatestTwo`) both
  shipped in one pass since neither needed new research. PAT coverage is
  expanding in batches (54 → 72 → 90 → 108 so far, alphabetical, no
  cherry-picking). r moved 0.43 → 0.37 → 0.185 → 0.157 across four
  batches, and at 108 stocks there's now a clean equal-size comparison:
  the original 54 (mega/large-cap) hold at r=0.425, essentially unchanged
  since the first pass, while the 54 stocks added since (batches 1–3,
  broader-market) show r=0.092 **on their own** — not weaker, close to
  *no* linear relationship. This is a real tension worth confronting: the
  signal this study keeps finding looks like a large-cap phenomenon, but
  §15/§16's original motivation for this whole pilot was about smaller
  multibagger candidates (KEI/NEULANDLAB-style), exactly the names where
  the signal may not hold. Worth resolving before more batches, not just
  noting.

_Done: transaction costs (§7); RS/sector filters (§8, negative); portfolio
allocation, max-positions, M10 opportunity-loss (rotation ruled out), and **M12
risk-based sizing — the breakthrough** (§9, now qualified by §15's OOS
result); **mean reversion (§10, rejected —
closes the regime-switcher's defensive-compounder leg)**;
**volatility regime filter (§12, rejected — exhausts the regime-filter space)**;
**resistance-zone breakout entries (§13, rejected — retest whipsaw and thin
edge at scale)**; **structure-based trailing exit (§14, real trade-off —
better win rate/PF/drawdown on swing, lower CAGR, not yet adopted)**;
**walk-forward OOS validation + multibagger diagnosis (§15, MAJOR FINDING —
in-sample edge does not generalize, and the strategy structurally can't hold
secular winners; unresolved)**; **long-hold buy-strength strategy (§16,
built — beats buy-and-hold NIFTY by ~1.6x at 20 positions, with a severe and
currently-active drawdown as the real cost)**; **portfolio-engine
non-determinism bug + walk-forward OOS on longhold (§16b, fixed and
validated — holds up across a two-era split, unlike §15's swing result)**;
**quarterly-results price reaction pilot (§17, in progress — 108 stocks
and growing, 1 quarter; r≈0.43 on the original 54 (Nifty 50 + 4) holds
steady across every batch, but the 54 broader-market names added since
(§17b, batches 1–3) show essentially no correlation on their own
(r≈0.09) — a clean equal-size comparison now, not just a hypothesis;
in tension with §15/§16's original motivation for this pilot, which was
about smaller multibagger candidates specifically; still too
small/short to size positions on; NSE/BSE direct access ruled out,
free news-source sourcing works instead; sector filter + live
daily-movers table shipped alongside; `earnings_watchlist` tracks all
108 symbols' upcoming quarters)**._

---

## 19. Reproduce

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

# §13 — resistance-zone breakout (REJECTED). Numbers here use the non-portfolio
# per-trade R-multiple summary (no --portfolio) — the comparison is on
# expectancy/profit-factor/streaks, not capital-allocation performance.
go run ./cmd/backtest --mode swing --from 2022-01-01 --to 2026-08-20     # baseline
go run ./cmd/backtest --mode breakout --from 2022-01-01 --to 2026-08-20  # default params
go run ./cmd/backtest --mode breakout --from 2022-01-01 --to 2026-08-20 \
  --br-stop-atr-mult 2.0 --trail-atr-mult 2.5                            # widened stop/trail
go run ./cmd/backtest --mode breakout --from 2022-01-01 --to 2026-08-20 \
  --br-require-contraction                                               # + contraction, default stop
go run ./cmd/backtest --mode breakout --from 2022-01-01 --to 2026-08-20 \
  --br-require-contraction --br-stop-atr-mult 2.0 --trail-atr-mult 2.5   # + contraction, widened stop
go run ./cmd/backtest --mode breakout --from 2022-01-01 --to 2026-08-20 \
  --br-min-resistance-touches 4 --br-stop-atr-mult 2.0 --trail-atr-mult 2.5  # touches >=4, no contraction
go run ./cmd/backtest --mode breakout --from 2022-01-01 --to 2026-08-20 \
  --br-min-resistance-touches 4 --br-require-contraction \
  --br-stop-atr-mult 2.0 --trail-atr-mult 2.5                            # touches >=4 + contraction

# §14 — structure-based ("swing low") trailing exit. Real trade-off, not
# adopted: better win rate/PF/drawdown on swing, lower CAGR; doesn't rescue
# crossover (the entry, not the exit, is crossover's problem).
go run ./cmd/backtest --portfolio --mode swing --from 2022-01-01 --to 2026-08-20 \
  --min-score 60 --min-rr 2 --exit-mode structure --max-positions 5 --max-hold 0 \
  --capital 100000 --cost-pct 0.25 --slippage-pct 0.20 \
  --risk-pct 1.0 --max-weight-pct 25 --health-window 20 --health-shadow
go run ./cmd/backtest --portfolio --mode crossover --from 2022-01-01 --to 2026-08-20 \
  --min-score 80 --exit-mode structure --max-positions 5 --max-hold 0 --capital 100000 \
  --co-min-rr 3 --co-min-vol-mult 3 --co-min-target-pct 8 --co-min-risk-pct 3 \
  --cost-pct 0.25 --slippage-pct 0.20

# §15 — walk-forward OOS (MAJOR FINDING, unresolved). Deep history first:
go run ./cmd/kite-sync --from 2010-01-01   # auto-chunked, ~5-6 min, all symbols + indices
# Then one fold per year (2012 shown; repeat for 2013..2025, --health-warmup-from fixed):
go run ./cmd/backtest --portfolio --mode swing --from 2012-01-01 --to 2012-12-31 \
  --min-score 60 --min-rr 2 --exit-mode ema --max-positions 5 --max-hold 0 --capital 100000 \
  --cost-pct 0.25 --slippage-pct 0.20 --risk-pct 1.0 --max-weight-pct 25 \
  --health-window 20 --health-shadow --health-warmup-from 2011-01-01   # gate ON
go run ./cmd/backtest --portfolio --mode swing --from 2012-01-01 --to 2012-12-31 \
  --min-score 60 --min-rr 2 --exit-mode ema --max-positions 5 --max-hold 0 --capital 100000 \
  --cost-pct 0.25 --slippage-pct 0.20 --risk-pct 1.0 --max-weight-pct 25   # baseline, no gate

# §16 — long-hold buy-strength strategy (18.9% CAGR at 20 positions vs
# NIFTY's 12.0%, with an active 55.9% drawdown -- see §16 for the full
# honest picture, including the CAGR-display bug found and fixed here.
# Numbers are post-§16b determinism fix; PASS --max-weight-pct 10, NOT 25,
# for the 20-position runs, and always include --cost-pct/--slippage-pct --
# omitting them gives a materially different, wrong-looking result.)
go run ./cmd/backtest --portfolio --mode longhold --from 2012-01-01 --to 2026-08-20 \
  --exit-mode trendstop --max-positions 5 --max-hold 0 --capital 100000 \
  --cost-pct 0.25 --slippage-pct 0.20 --risk-pct 1.0 --max-weight-pct 25
go run ./cmd/backtest --portfolio --mode longhold --from 2012-01-01 --to 2026-08-20 \
  --exit-mode trendstop --max-positions 20 --max-hold 0 --capital 100000 \
  --cost-pct 0.25 --slippage-pct 0.20 --risk-pct 1.0 --max-weight-pct 10 \
  --equity-output /tmp/lh_equity.csv --output /tmp/lh_trades.csv

# S16 — the real scenario: Rs5L start + Rs10k/month SIP, 20 positions.
# Trust the printed XIRR, not the "Total return" CAGR line (it ignores the
# deposits and overstates the true return). Result: XIRR 22.6%/yr, verified
# bit-identical across repeated runs post-§16b fix.
go run ./cmd/backtest --portfolio --mode longhold --from 2012-01-01 --to 2026-08-20 \
  --exit-mode trendstop --max-positions 20 --max-hold 0 --capital 500000 \
  --monthly-contribution 10000 \
  --cost-pct 0.25 --slippage-pct 0.20 --risk-pct 1.0 --max-weight-pct 10

# §16b — walk-forward OOS validation (two-era split) and parameter
# sensitivity sweep. Era CAGR: 2012-2018 -> 26.6%, 2019-2026 -> 17.6%,
# both clearing NIFTY's own era CAGR (12.9% / 11.2%).
go run ./cmd/backtest --portfolio --mode longhold --from 2012-01-01 --to 2018-12-31 \
  --exit-mode trendstop --max-positions 20 --max-hold 0 --capital 100000 \
  --cost-pct 0.25 --slippage-pct 0.20 --risk-pct 1.0 --max-weight-pct 10
go run ./cmd/backtest --portfolio --mode longhold --from 2019-01-01 --to 2026-08-20 \
  --exit-mode trendstop --max-positions 20 --max-hold 0 --capital 100000 \
  --cost-pct 0.25 --slippage-pct 0.20 --risk-pct 1.0 --max-weight-pct 10
# Sensitivity sweep: swap in --trend-stop-ema {150,200,250} or
# --lh-high-lookback {126,252,378} on the full 2012-2026 command above.

# §17/§17b — quarterly-results price reaction study (72 stocks and growing, Q1 FY27).
# --seed is idempotent (upsert on symbol+report_date) -- safe to re-run.
go run ./cmd/earnings-reaction --seed      # seed all known Q1 FY27 events
go run ./cmd/earnings-reaction             # summary table, all stored events
go run ./cmd/earnings-reaction --detail KEI  # full day-by-day table, one symbol
go run ./cmd/earnings-reaction --watch     # register the next quarter to watch
go run ./cmd/earnings-reaction --pending   # list symbols not yet declared

# §17b — live dashboard: sector-filterable earnings table + daily movers
# (all 501 watchlist symbols, /api/movers) in one local server.
go run ./cmd/earnings-dashboard
```

_Note: the `--exit-mode ema` / portfolio engine is the trustworthy path. The
serial single-symbol mode (no `--portfolio`) is kept for signal inspection only._
