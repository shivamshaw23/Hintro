// Package handler contains HTTP request handlers for the ride pooling API.
package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/shiva/hintro/internal/service"
)

// MatchHandler handles ride matching HTTP requests.
type MatchHandler struct {
	matcher *service.MatchingService
}

// NewMatchHandler creates a new handler wired to the matching service.
func NewMatchHandler(matcher *service.MatchingService) *MatchHandler {
	return &MatchHandler{matcher: matcher}
}

// MatchRideRequest handles POST /api/v1/match/{request_id}
//
// Attempts to find an existing trip for the given ride request.
// Returns 200 with match details, or 404 if no compatible trip exists.
func (h *MatchHandler) MatchRideRequest(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	requestID, err := strconv.ParseInt(vars["request_id"], 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid request_id: must be an integer",
		})
		return
	}

	result, err := h.matcher.MatchRiders(r.Context(), requestID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNoMatch):
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":   "no_match",
				"message": "No compatible trip found. A new trip should be created.",
			})
		case errors.Is(err, service.ErrRequestNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":   "not_found",
				"message": "Ride request not found.",
			})
		case errors.Is(err, service.ErrAlreadyMatched):
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":   "already_matched",
				"message": "This ride request is already matched to a trip.",
			})
		default:
			log.Printf("[handler] match error: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "internal_error",
			})
		}
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// writeJSON is a helper that writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
