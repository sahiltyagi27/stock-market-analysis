#!/usr/bin/env bash
# Install (or uninstall) the launchd job that runs the EOD paper-trade cycle
# automatically at 15:45 IST Mon–Fri.
#
# Usage:
#   scripts/install-launchd.sh             # install
#   scripts/install-launchd.sh --uninstall # remove
#
# After installing, the job fires automatically every weekday at 15:45 IST.
# Logs land in ~/.stockmarket/logs/.
#
# The Kite token still needs a daily manual refresh (Kite's OAuth requires a
# browser login). Do it before 15:45 each trading day:
#   go run ./cmd/kite-token
set -euo pipefail

LABEL="com.sahiltyagi27.stockmarket.eod"
PLIST_SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/com.sahiltyagi27.stockmarket.eod.plist"
PLIST_DST="$HOME/Library/LaunchAgents/$LABEL.plist"
PROJ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG_DIR="$HOME/.stockmarket/logs"
EOD_SH="$PROJ/scripts/eod.sh"

uninstall() {
    if launchctl list "$LABEL" &>/dev/null 2>&1; then
        launchctl unload "$PLIST_DST" 2>/dev/null || true
        echo "Unloaded $LABEL"
    fi
    rm -f "$PLIST_DST"
    echo "Removed $PLIST_DST"
}

if [[ "${1:-}" == "--uninstall" ]]; then
    uninstall
    exit 0
fi

# ── Preflight checks ──────────────────────────────────────────────────────────
if [[ ! -f "$PLIST_SRC" ]]; then
    echo "ERROR: plist template not found: $PLIST_SRC"
    exit 1
fi
if [[ ! -f "$EOD_SH" ]]; then
    echo "ERROR: eod.sh not found: $EOD_SH"
    exit 1
fi
chmod +x "$EOD_SH"

# ── Stamp paths into the plist ────────────────────────────────────────────────
mkdir -p "$LOG_DIR" "$HOME/Library/LaunchAgents"

sed \
    -e "s|PLACEHOLDER_EOD_SH|$EOD_SH|g" \
    -e "s|PLACEHOLDER_LOG_DIR|$LOG_DIR|g" \
    -e "s|PLACEHOLDER_PROJ|$PROJ|g" \
    "$PLIST_SRC" > "$PLIST_DST"

# ── Load (reload if already installed) ───────────────────────────────────────
launchctl unload "$PLIST_DST" 2>/dev/null || true
launchctl load "$PLIST_DST"

echo ""
echo "✓ EOD job installed and active."
echo ""
echo "  Fires:  Mon–Fri at 15:45 IST (assumes Mac timezone = IST)"
echo "  Script: $EOD_SH"
echo "  Plist:  $PLIST_DST"
echo "  Logs:   $LOG_DIR/eod-YYYYMMDD.log"
echo ""
echo "Daily token refresh (do this before 15:45 each trading day):"
echo "  go run ./cmd/kite-token"
echo ""
echo "Test the script right now (dry-run):"
echo "  scripts/eod.sh --dry-run"
echo ""
echo "Uninstall:"
echo "  scripts/install-launchd.sh --uninstall"
