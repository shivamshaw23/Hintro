package handler

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/shiva/hintro/internal/model"
	"github.com/shiva/hintro/internal/service"
)

// FareRequest is the JSON body for POST /api/v1/fare/estimate.
type FareRequest struct {
	OriginLat float64 `json:"origin_lat"`
	OriginLon float64 `json:"origin_lon"`
	DestLat   float64 `json:"dest_lat"`
	DestLon   float64 `json:"dest_lon"`
}

// PricingHandler handles fare estimation HTTP requests.
type PricingHandler struct {
	pricingSvc *service.PricingService
}

// NewPricingHandler creates a new pricing handler.
func NewPricingHandler(pricingSvc *service.PricingService) *PricingHandler {
	return &PricingHandler{pricingSvc: pricingSvc}
}

// EstimateFare handles POST /api/v1/fare/estimate
//
// Request body:
//
//	{
//	  "origin_lat": 28.7041, "origin_lon": 77.1025,
//	  "dest_lat": 28.5562,   "dest_lon": 77.0889
//	}
//
// Response: FareEstimate with breakdown and surge info.
func (h *PricingHandler) EstimateFare(w http.ResponseWriter, r *http.Request) {
	var req FareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid JSON body",
		})
		return
	}

	// Basic validation.
	if req.OriginLat == 0 || req.OriginLon == 0 || req.DestLat == 0 || req.DestLon == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "origin_lat, origin_lon, dest_lat, and dest_lon are all required",
		})
		return
	}

	origin := model.Location{Lat: req.OriginLat, Lon: req.OriginLon}
	dest := model.Location{Lat: req.DestLat, Lon: req.DestLon}

	estimate, err := h.pricingSvc.EstimateFare(r.Context(), origin, dest)
	if err != nil {
		log.Printf("[handler] pricing error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to estimate fare",
		})
		return
	}

	writeJSON(w, http.StatusOK, estimate)
}
