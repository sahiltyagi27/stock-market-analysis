package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sahiltyagi27/stock-market-analysis/internal/service"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
)

type Handler struct {
	svc *service.CandleService
}

func NewHandler(svc *service.CandleService) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/stocks/{symbol}/candles", h.getCandles)
	r.Get("/stocks/{symbol}/latest", h.getLatest)
	return r
}

// GET /stocks/{symbol}/candles?from=2024-01-01&to=2024-12-31&limit=100
func (h *Handler) getCandles(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(chi.URLParam(r, "symbol"))
	q := r.URL.Query()

	var f store.CandleFilter
	if s := q.Get("from"); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid 'from' date, expected YYYY-MM-DD")
			return
		}
		f.From = &t
	}
	if s := q.Get("to"); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid 'to' date, expected YYYY-MM-DD")
			return
		}
		f.To = &t
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid 'limit'")
			return
		}
		f.Limit = n
	}

	candles, err := h.svc.GetCandles(r.Context(), symbol, f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch candles")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"symbol":  symbol,
		"count":   len(candles),
		"candles": candles,
	})
}

// GET /stocks/{symbol}/latest
func (h *Handler) getLatest(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(chi.URLParam(r, "symbol"))

	candle, err := h.svc.GetLatest(r.Context(), symbol)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch latest candle")
		return
	}
	if candle == nil {
		writeError(w, http.StatusNotFound, "no data found for symbol "+symbol)
		return
	}
	writeJSON(w, http.StatusOK, candle)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
