package handler

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/shiva/hintro/internal/service"
)

// BookingHandler handles booking HTTP requests.
type BookingHandler struct {
	bookingSvc *service.BookingService
}

// NewBookingHandler creates a new booking handler.
func NewBookingHandler(bookingSvc *service.BookingService) *BookingHandler {
	return &BookingHandler{bookingSvc: bookingSvc}
}

// BookRide handles POST /api/v1/book/{request_id}
//
// Attempts to book a ride for the given request. If a compatible trip exists,
// the passenger is added to it. Otherwise, a new trip is created.
//
// Response codes:
//   200  — Booking successful (returns booking details)
//   400  — Invalid request_id
//   404  — Ride request not found
//   409  — Request already booked / not in pending state
//   422  — Cab full (capacity exceeded) or no cab available
//   408  — Booking timed out (lock contention)
//   500  — Unexpected error
func (h *BookingHandler) BookRide(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	requestID, err := strconv.ParseInt(vars["request_id"], 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid request_id: must be an integer",
		})
		return
	}

	result, err := h.bookingSvc.BookRide(r.Context(), requestID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrCabFull):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error":   "cab_full",
				"message": "The cab has no remaining capacity. Try again for another cab.",
			})
		case errors.Is(err, service.ErrBookingTimeout):
			writeJSON(w, http.StatusRequestTimeout, map[string]string{
				"error":   "booking_timeout",
				"message": "Booking timed out due to high contention. Please retry.",
			})
		case errors.Is(err, service.ErrRequestNotPending):
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":   "not_pending",
				"message": "This ride request is not in a bookable state.",
			})
		case errors.Is(err, service.ErrCabNotAvailable):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error":   "cab_unavailable",
				"message": "The assigned cab is no longer available.",
			})
		case errors.Is(err, service.ErrNoCabNearby):
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":   "no_cab",
				"message": "No available cab found near your pickup location.",
			})
		case errors.Is(err, service.ErrRequestNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":   "not_found",
				"message": "Ride request not found.",
			})
		default:
			log.Printf("[handler] booking error: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "internal_error",
			})
		}
		return
	}

	writeJSON(w, http.StatusOK, result)
}
