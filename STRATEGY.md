# The Strategy, Explained

A plain-English guide to *what* this engine trades, *why*, and *how* the pieces
fit together. For the full research narrative behind every decision — including
the dead ends — see **[ANALYSIS.md](ANALYSIS.md)**.

---

## In one sentence

> **Buy quality stocks pulling back to support in an uptrend, size each bet by its
> risk, ride the trend until momentum turns, and only trade while the strategy
> itself is working.**

It is a **daily swing strategy**: decisions are made once a day on *closed* daily
candles, and trades typically last 2–4 weeks. Nothing intraday.

---

## 1. How a stock becomes a candidate (the entry)

A stock must pass a chain of filters — each one a question a disciplined swing
trader would ask:

| Filter | The question it answers |
|---|---|
| Price > EMA50 **and** > EMA200, with EMA200 **rising** | Is this a genuine uptrend (not just briefly above a falling average)? |
| Near a **support zone** tested ≥ 2 times | Is it pulling *back* to a level buyers defended before — on sale, not chasing? |
| **Bullish candle** at the zone | Is it actually bouncing today, not still falling into support? |
| **Risk / reward ≥ 2** | Is the target at least 2× the distance to the stop? |
| **Stop 1.5–8% away** | Not so tight it is market noise, not so wide it is a different trade |
| Liquidity + not over-extended | Can I exit cleanly? Has it already run too far? |

- The **stop-loss** sits below the support zone, distance scaled by **ATR** so it
  adapts to each stock's volatility.
- The **target** is the next resistance zone.
- Each surviving candidate gets a **score** (trend + R/R + support strength +
  volume) and they are ranked.

**Core principle:** *buy pullbacks in strong trends.* Not "buy strength"
(chasing), not "buy weakness" (catching falling knives) — buy a **temporary dip
in something that is already winning.**

---

## 2. The four layers that make it work

The biggest lesson of the whole project:

> The entry signal got the strategy from **terrible → mediocre**.
> Portfolio construction got it from **mediocre → good.**

Four layers, ordered by how much they actually mattered:

### ① Exit — ride the trend, don't take a fixed target
Instead of selling at the pre-set resistance target, **hold until the 7-day EMA
crosses back below the 21-day EMA** (or the stop is hit). A fixed target caps your
winners, and the rare giant trends are where the money is. Letting winners run
beat every entry tweak we tried.

### ② Position sizing — risk a fixed 1% per trade
Each position is sized so that **if the stop is hit, you lose ~1% of the account.**
A tight-stop stock gets more capital; a volatile, wide-stop stock gets less
(capped at 25% of equity). This was the breakthrough that improved **return and
drawdown at the same time** — you automatically bet small on wobbly names and
larger on steady ones.

### ③ The strategy-health gate
The single most important risk control — explained in §3 below.

### ④ Costs are real
Every trade pays ~0.25% (brokerage + STT + fees) plus ~0.2% slippage. Modeling
this keeps the backtest honest — and is exactly why a high-frequency EMA-crossover
variant was **rejected**: it traded too often and bled fees.

---

## 3. The strategy-health gate (the key idea)

### The problem
The strategy makes money in good regimes and loses in bad ones (2023 was +70%;
2024 and 2025 were small losses). The obvious fix is a **market filter** — "only
trade when NIFTY is healthy." **We tried it. It failed.** The index can look
perfectly fine while the *specific stocks this strategy picks* are bleeding.
**Market direction is not the strategy's risk.**

### The insight
The strategy itself knows when it is working — better than any index does. So
instead of watching the market, **watch your own recent trades.**

### The mechanism
- Keep a rolling window of the **last 20 closed trades**.
- Compute their **average R** (R = result in units of the risk taken; +2R doubled
  the risk, −1R hit the stop).
- **Average positive → strategy is working → keep taking new trades.**
- **Average negative → strategy is in a bad patch → stop opening new positions**
  (open trades are left to play out).
- As in-flight trades close as winners again, the average flips positive and the
  gate reopens.

### Why it works
It is a feedback loop on the strategy's own equity curve. In a good regime it
stays open and barely matters (2023 ran identically with or without it). In a bad
regime it shuts the tap — which **roughly halved the losing years** (2024:
−14% → −8.5%) while leaving the good years untouched. Higher return, lower
drawdown, together. It also held up **out-of-sample** (validated on each half of
the data independently).

### Warm-starting (seeding)
On day one there is no recent history, so the gate cannot judge. `--seed-from`
fills the window with the last 20 trades from a backtest, so the gate starts in
the *right* state. In testing it correctly started **closed** because the recent
regime was weak — protecting capital before the first live trade.

---

## 4. The daily decision

Every evening after the close:

1. **Fill** yesterday's queued entries at today's open (sized at 1% risk).
2. **Check exits** on holdings: stop hit? EMA7 crossed below EMA21? If so, sell
   and record the result into trade history.
3. **Update the health gate** from that history.
4. **If the gate is open and a slot is free** (max 5 positions): scan all ~500
   stocks, take the best-scoring new setups, queue them for tomorrow's open.
5. Persist everything.

Five positions max — tested: more diluted returns, fewer concentrated risk too
much. Five was the sweet spot.

---

## 5. The honest scorecard

Full stack (swing + EMA-hold exit + 1% sizing + health gate, **after costs**),
2022–2025:

| | This strategy | NIFTY buy-and-hold |
|---|---|---|
| CAGR | ~14.7%/yr | ~10%/yr |
| Max drawdown | −12.5% | ~−15% |
| Profit factor | 2.58 | — |

It modestly **beats the index on a risk-adjusted basis.** The caveats, stated
plainly:

- **Regime-dependent.** One great year (2023, +70%) carried the result; other
  years were flat-to-slightly-negative. The gate makes bad years *less bad* — it
  cannot manufacture an edge that is not there.
- **One ~4-year sample.** Validated out-of-sample as far as the data allows, not
  battle-tested across a decade.
- **Win rate ~30%.** It profits because winners are ~3× the size of losers, not
  because it is often right. Long strings of small losses are normal for this
  style — the gate and sizing are what make that survivable.

---

## 6. How you run it

| Tool | Purpose |
|---|---|
| `cmd/scan`, `cmd/live-scan` | See today's candidate setups (entry half — offline or live) |
| `cmd/backtest` | Prove the strategy on history with realistic costs (where everything above was validated) |
| `cmd/paper-trade` | Run the **whole** validated stack forward with persistent state — `--mode live` to monitor, `--mode eod` to execute the daily cycle |

---

## The principle that ties it together

We stopped asking *"what is a better signal?"* and started asking *"given a decent
signal, how do you build a portfolio around it that survives bad regimes?"*

The answer — **risk-based sizing + an equity-curve health gate** — is where the
real edge came from. The signal gets you into the game; the construction is what
keeps you in it.
