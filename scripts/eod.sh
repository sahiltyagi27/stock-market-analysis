#!/usr/bin/env bash
# EOD paper-trade cycle: validate token → sync today's candle → run eod engine.
#
# Usage:
#   scripts/eod.sh              # normal run (appends to dated log file)
#   scripts/eod.sh --dry-run    # preview eod without writing to DB
#
# Logs are written to ~/.stockmarket/logs/eod-YYYYMMDD.log
# Run this after the NSE close (15:30 IST). The launchd plist fires it at 15:45.
#
# If the Kite token has expired the script exits early with instructions.
# Refresh it with:  go run ./cmd/kite-token
set -euo pipefail

PROJ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG_DIR="$HOME/.stockmarket/logs"
mkdir -p "$LOG_DIR"
LOG="$LOG_DIR/eod-$(date +%Y%m%d).log"
DRY_RUN=""
[[ "${1:-}" == "--dry-run" ]] && DRY_RUN="--dry-run"

log() { echo "[$(date '+%H:%M:%S')] $*" | tee -a "$LOG"; }

cd "$PROJ"

# Make sure Go is on PATH (launchd doesn't inherit the shell PATH).
for gopath in /opt/homebrew/bin /usr/local/go/bin /usr/local/bin "$HOME/go/bin"; do
    [[ -x "$gopath/go" ]] && export PATH="$gopath:$PATH" && break
done

if ! command -v go &>/dev/null; then
    log "ERROR: go binary not found — add it to PATH or update the gopath search list in scripts/eod.sh"
    exit 1
fi

log "=== EOD cycle starting ($(date '+%Y-%m-%d')) ==="
[[ -n "$DRY_RUN" ]] && log "(dry-run mode — paper-trade will not write)"

# ── 1. Validate Kite token ────────────────────────────────────────────────────
log "Checking Kite token…"
if ! go run ./cmd/kite-token --check >> "$LOG" 2>&1; then
    log "ERROR: Kite token expired or missing."
    log "       Refresh it with:  go run ./cmd/kite-token"
    log "       Then re-run:      scripts/eod.sh"
    exit 1
fi

# ── 2. Sync today's candle ────────────────────────────────────────────────────
log "Syncing candles (last 30 days)…"
go run ./cmd/kite-sync --period 30d 2>&1 | tee -a "$LOG"

# ── 3. Run EOD paper-trade cycle ─────────────────────────────────────────────
log "Running paper-trade EOD…"
# shellcheck disable=SC2086
go run ./cmd/paper-trade --mode eod $DRY_RUN 2>&1 | tee -a "$LOG"

log "=== EOD cycle complete ==="
log "Log: $LOG"
