package handler

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/shiva/hintro/internal/service"
)

// CancelHandler handles ride cancellation HTTP requests.
type CancelHandler struct {
	cancelSvc *service.CancelService
}

// NewCancelHandler creates a new cancel handler.
func NewCancelHandler(cancelSvc *service.CancelService) *CancelHandler {
	return &CancelHandler{cancelSvc: cancelSvc}
}

// CancelRide handles POST /api/v1/cancel/{request_id}
//
// Cancels a ride request. Only PENDING and MATCHED requests can be cancelled.
//
// Response codes:
//
//	200 — Cancellation successful
//	400 — Invalid request_id
//	404 — Ride request not found
//	409 — Request already cancelled or in non-cancellable state
func (h *CancelHandler) CancelRide(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	requestID, err := strconv.ParseInt(vars["request_id"], 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid request_id: must be an integer",
		})
		return
	}

	result, err := h.cancelSvc.CancelRide(r.Context(), requestID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrAlreadyCancelled):
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":   "already_cancelled",
				"message": "This ride request is already cancelled.",
			})
		case errors.Is(err, service.ErrCannotCancel):
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":   "cannot_cancel",
				"message": "This ride request cannot be cancelled (confirmed or completed).",
			})
		case errors.Is(err, service.ErrRequestNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":   "not_found",
				"message": "Ride request not found.",
			})
		default:
			log.Printf("[handler] cancel error: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "internal_error",
			})
		}
		return
	}

	// Build response (exclude internal fields like OriginLat/OriginLon).
	resp := map[string]interface{}{
		"request_id": result.RequestID,
	}
	if result.PreviousTrip != nil {
		resp["previous_trip_id"] = *result.PreviousTrip
	}
	if result.TripCancelled {
		resp["trip_cancelled"] = true
	}
	if result.CabFreed {
		resp["cab_freed"] = true
	}

	writeJSON(w, http.StatusOK, resp)
}
