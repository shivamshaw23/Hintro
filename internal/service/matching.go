// Package service contains the core business logic for ride pooling.
package service

import (
	"context"
	"errors"
	"log"
	"math"

	"github.com/shiva/hintro/internal/model"
	"github.com/shiva/hintro/internal/repository"
	"github.com/shiva/hintro/pkg/geo"
)

// ─── Errors ─────────────────────────────────────────────────

var (
	ErrNoMatch        = errors.New("no matching trip found; a new trip should be created")
	ErrRequestNotFound = errors.New("ride request not found")
	ErrAlreadyMatched  = errors.New("ride request is already matched to a trip")
)

// ─── Constants ──────────────────────────────────────────────

const (
	// DefaultSearchRadiusM is the spatial radius for finding candidate
	// trips/requests (2 km). Matches the default tolerance_meters in schema.
	DefaultSearchRadiusM = 2000

	// MaxCandidates caps the number of candidate trips to evaluate.
	// Keeps the inner loop bounded for latency guarantees.
	MaxCandidates = 20

	// MaxDetourMinutes is the hard ceiling for any single passenger's detour.
	MaxDetourMinutes = 15.0
)

// ─── MatchingService ────────────────────────────────────────

// MatchingService implements the Greedy Heuristic ride matching algorithm.
//
// Algorithm overview (for airport pooling — Many-to-One / One-to-Many):
//
//  1. FETCH: Use PostGIS ST_DWithin (GIST index) to find nearby planned trips.
//  2. FILTER: Hard constraint check — seats + luggage capacity.
//  3. SCORE: For each candidate, simulate inserting the new pickup into the
//     route and calculate the added detour (using Haversine estimation).
//  4. SELECT: Pick the trip with the LEAST added detour that doesn't violate
//     any existing passenger's tolerance.
//
// Time Complexity:
//
//	O(C × S) where C = candidates (≤ 20, capped) and S = stops per trip (≤ 6).
//	With GIST index on origin, the DB fetch is O(log N).
//	Total per request: O(log N + C × S) — well under 1ms for typical inputs.
type MatchingService struct {
	Repo *repository.RideRepository
}

// NewMatchingService creates a matching service backed by the given repository.
func NewMatchingService(repo *repository.RideRepository) *MatchingService {
	return &MatchingService{Repo: repo}
}

// MatchRiders attempts to find an existing trip for the given ride request.
//
// Returns a MatchResult if a compatible trip is found, or ErrNoMatch if the
// request should seed a new trip.
//
// This function is safe to call concurrently — all mutable state lives in
// PostgreSQL with row-level locking.
func (s *MatchingService) MatchRiders(ctx context.Context, requestID int64) (*model.MatchResult, error) {
	// ── Step 0: Fetch the ride request ──────────────────
	req, err := s.Repo.GetRideRequest(ctx, requestID, false)
	if err != nil {
		return nil, ErrRequestNotFound
	}

	if req.Status != model.RequestPending {
		return nil, ErrAlreadyMatched
	}

	log.Printf("[match] Processing request #%d: origin=(%.4f,%.4f) dir=%s seats=%d luggage=%d",
		req.ID, req.Origin.Lat, req.Origin.Lon, req.Direction, req.SeatsNeeded, req.LuggageCount)

	// ── Step 1: FETCH nearby candidate trips (PostGIS) ──
	// Uses GIST index on ride_requests(origin) via ST_DWithin.
	searchRadius := req.ToleranceMeters
	if searchRadius <= 0 {
		searchRadius = DefaultSearchRadiusM
	}

	candidates, err := s.Repo.FindNearbyCandidateTrips(ctx, req.Origin, req.Direction, searchRadius)
	if err != nil {
		return nil, err
	}

	log.Printf("[match] Found %d candidate trips within %dm", len(candidates), searchRadius)

	if len(candidates) == 0 {
		return nil, ErrNoMatch
	}

	// ── Step 2 + 3: FILTER & SCORE ──────────────────────
	// Greedy: evaluate each candidate, keep the best.
	bestScore := math.MaxFloat64
	var bestMatch *model.MatchResult

	for i := range candidates {
		ct := &candidates[i]

		// --- Hard Constraint: Seat capacity ---
		if ct.CurrentLoad+req.SeatsNeeded > ct.SeatCapacity {
			log.Printf("[match]   Trip #%d: SKIP seats (%d+%d > %d)",
				ct.TripID, ct.CurrentLoad, req.SeatsNeeded, ct.SeatCapacity)
			continue
		}

		// --- Hard Constraint: Luggage capacity ---
		if ct.CurrentLuggage+req.LuggageCount > ct.LuggageCapacity {
			log.Printf("[match]   Trip #%d: SKIP luggage (%d+%d > %d)",
				ct.TripID, ct.CurrentLuggage, req.LuggageCount, ct.LuggageCapacity)
			continue
		}

		// --- Detour Calculation ---
		detour, valid := s.calculateDetour(ctx, ct, req)
		if !valid {
			log.Printf("[match]   Trip #%d: SKIP detour exceeds tolerance", ct.TripID)
			continue
		}

		log.Printf("[match]   Trip #%d: detour=%.2f min (current best=%.2f)",
			ct.TripID, detour, bestScore)

		// --- Greedy selection: lowest detour wins ---
		if detour < bestScore {
			bestScore = detour
			bestMatch = &model.MatchResult{
				TripID:      ct.TripID,
				CabID:       ct.CabID,
				AddedDetour: detour,
			}
		}
	}

	if bestMatch != nil {
		log.Printf("[match] ✓ Best match: trip #%d with %.2f min detour", bestMatch.TripID, bestMatch.AddedDetour)
		return bestMatch, nil
	}

	return nil, ErrNoMatch
}

// calculateDetour checks if adding the new rider to the trip violates any
// passenger's tolerance, and returns the added time in minutes.
//
// Strategy:
//  1. Fetch the current trip route (ordered stops + destination).
//  2. Use FindBestInsertionIndex to find optimal pickup position.
//  3. Check if the added time exceeds the new rider's tolerance.
//  4. Check if the added time exceeds the global MaxDetourMinutes.
//
// Complexity: O(S²) where S = stops (≤ 6), so effectively O(1).
func (s *MatchingService) calculateDetour(
	ctx context.Context,
	trip *model.CandidateTrip,
	req *model.RideRequest,
) (float64, bool) {
	// If the trip has no existing route, the detour is zero
	// (this is the first pickup being added).
	if len(trip.Route) < 2 {
		return 0, true
	}

	// Find the best spot to insert the new passenger's origin.
	_, addedMinutes := geo.FindBestInsertionIndex(trip.Route, req.Origin)

	// Check 1: Does this exceed the NEW rider's tolerance?
	// Convert tolerance from meters to approximate minutes.
	toleranceMinutes := float64(req.ToleranceMeters) / 1000.0 / geo.AverageSpeedKmph * 60.0
	if addedMinutes > toleranceMinutes {
		return 0, false
	}

	// Check 2: Does it exceed the hard detour ceiling?
	if addedMinutes > MaxDetourMinutes {
		return 0, false
	}

	return addedMinutes, true
}
