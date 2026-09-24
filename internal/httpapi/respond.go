package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode response", "err", err)
	}
}

func respondData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, map[string]any{"data": data})
}

type listMeta struct {
	Total   int  `json:"total"`
	Page    int  `json:"page"`
	Limit   int  `json:"limit"`
	HasMore bool `json:"hasMore"`
}

func respondList(w http.ResponseWriter, data any, meta listMeta) {
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "meta": meta})
}

func respondError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": errorBody{Code: code, Message: message}})
}

func respondInternal(w http.ResponseWriter, err error) {
	slog.Error("internal error", "err", err)
	respondError(w, http.StatusInternalServerError, "internal", "Internal server error")
}
