package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/shiva/hintro/internal/model"
	"github.com/shiva/hintro/internal/repository"
)

// ─── Request/Response DTOs ──────────────────────────────────

// CreateRideRequestBody is the JSON body for POST /api/v1/rides.
type CreateRideRequestBody struct {
	UserID          int64   `json:"user_id"`
	OriginLat       float64 `json:"origin_lat"`
	OriginLon       float64 `json:"origin_lon"`
	DestLat         float64 `json:"dest_lat"`
	DestLon         float64 `json:"dest_lon"`
	Direction       string  `json:"direction"`
	SeatsNeeded     int     `json:"seats_needed"`
	LuggageCount    int     `json:"luggage_count"`
	ToleranceMeters int     `json:"tolerance_meters"`
}

// ─── RideHandler ────────────────────────────────────────────

// RideHandler handles ride request CRUD and cancellation.
type RideHandler struct {
	repo *repository.RideRequestRepository
}

// NewRideHandler creates a new ride handler.
func NewRideHandler(repo *repository.RideRequestRepository) *RideHandler {
	return &RideHandler{repo: repo}
}

// CreateRide handles POST /api/v1/rides
//
// Creates a new pending ride request.
//
//	Request body:
//	{
//	  "user_id": 1,
//	  "origin_lat": 28.7041, "origin_lon": 77.1025,
//	  "dest_lat": 28.5562, "dest_lon": 77.0889,
//	  "direction": "to_airport",
//	  "seats_needed": 1, "luggage_count": 1,
//	  "tolerance_meters": 2000
//	}
func (h *RideHandler) CreateRide(w http.ResponseWriter, r *http.Request) {
	var body CreateRideRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid JSON body",
		})
		return
	}

	// Validation
	if body.UserID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	if body.OriginLat == 0 || body.OriginLon == 0 || body.DestLat == 0 || body.DestLon == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "origin and destination coordinates are required"})
		return
	}
	if body.Direction != "to_airport" && body.Direction != "from_airport" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "direction must be 'to_airport' or 'from_airport'"})
		return
	}
	if body.SeatsNeeded <= 0 {
		body.SeatsNeeded = 1
	}
	if body.LuggageCount < 0 {
		body.LuggageCount = 0
	}
	if body.LuggageCount > model.MaxLuggagePerRequest {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "luggage_count must be between 0 and 8",
		})
		return
	}
	if body.ToleranceMeters <= 0 {
		body.ToleranceMeters = 2000 // Default 2km
	}

	req := &model.RideRequest{
		UserID:          body.UserID,
		Origin:          model.Location{Lat: body.OriginLat, Lon: body.OriginLon},
		Destination:     model.Location{Lat: body.DestLat, Lon: body.DestLon},
		Direction:       model.TripDirection(body.Direction),
		SeatsNeeded:     body.SeatsNeeded,
		LuggageCount:    body.LuggageCount,
		ToleranceMeters: body.ToleranceMeters,
	}

	created, err := h.repo.CreateRideRequest(r.Context(), req)
	if err != nil {
		log.Printf("[handler] create ride error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to create ride request",
		})
		return
	}

	writeJSON(w, http.StatusCreated, created)
}

// GetRide handles GET /api/v1/rides/{id}
//
// Returns the current status of a ride request.
func (h *RideHandler) GetRide(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid ride id",
		})
		return
	}

	rideReq, err := h.repo.GetRideRequestByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "ride request not found",
		})
		return
	}

	writeJSON(w, http.StatusOK, rideReq)
}

// CancelRide handles POST /api/v1/rides/{id}/cancel
//
// Cancels a pending or matched ride request, releasing the seat
// back to the cab atomically (pessimistic locking).
func (h *RideHandler) CancelRide(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid ride id",
		})
		return
	}

	err = h.repo.CancelRideRequest(r.Context(), id)
	if err != nil {
		errMsg := err.Error()
		// Not found
		if errors.Is(err, errors.New("no rows")) || containsAny(errMsg, "no rows", "lock request") {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":   "not_found",
				"message": "Ride request not found.",
			})
			return
		}
		// Already completed/cancelled
		if containsAny(errMsg, "cannot cancel") {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":   "not_cancellable",
				"message": "Ride request is not in a cancellable state.",
			})
			return
		}
		log.Printf("[handler] cancel ride error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to cancel ride request",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "cancelled",
		"message": "Ride request cancelled successfully. Seat released.",
	})
}

// GetTrip handles GET /api/v1/trips/{id}
//
// Returns trip details with its passenger list.
func (h *RideHandler) GetTrip(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid trip id",
		})
		return
	}

	trip, passengers, err := h.repo.GetTripByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "trip not found",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"trip":       trip,
		"passengers": passengers,
	})
}

// containsAny checks if s contains any of the substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
