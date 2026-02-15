package service

import (
	"context"
	"log"
	"math"

	"github.com/shiva/hintro/internal/model"
	"github.com/shiva/hintro/internal/repository"
	"github.com/shiva/hintro/pkg/geo"
)

// ─── Fare Configuration ─────────────────────────────────────

// FareConfig holds the pricing parameters.
// In production, these would come from a config file or database.
type FareConfig struct {
	BaseFareCents    int     // Fixed base fare in cents (e.g., ₹50 = 5000 paisa).
	PerKmRateCents   int     // Rate per kilometer in cents (e.g., ₹12/km = 1200).
	PerMinRateCents  int     // Rate per minute in cents (e.g., ₹2/min = 200).
	MinFareCents     int     // Minimum fare floor in cents.
	SurgeRadiusM     int     // Radius in meters for demand/supply calculation.
}

// DefaultFareConfig returns sensible defaults for Indian airport rides.
func DefaultFareConfig() FareConfig {
	return FareConfig{
		BaseFareCents:   5000,  // ₹50 base fare
		PerKmRateCents:  1200,  // ₹12 per km
		PerMinRateCents: 200,   // ₹2 per minute
		MinFareCents:    7500,  // ₹75 minimum
		SurgeRadiusM:    5000,  // 5km surge zone
	}
}

// ─── Surge Thresholds ───────────────────────────────────────
//
// Surge multiplier is determined by the Demand/Supply ratio (R):
//
//   R ≤ 1.5  →  1.0x  (no surge)
//   R > 1.5  →  1.2x  (moderate surge)
//   R > 2.0  →  1.5x  (high surge)
//
// This is a tiered step function. In production, you could use a
// continuous function like min(1.0 + 0.25*(R-1), 3.0) for smoother pricing.

const (
	SurgeThresholdModerate = 1.5
	SurgeThresholdHigh     = 2.0

	SurgeMultiplierNone     = 1.0
	SurgeMultiplierModerate = 1.2
	SurgeMultiplierHigh     = 1.5
)

// ─── FareEstimate ───────────────────────────────────────────

// FareEstimate is the response from the pricing service.
type FareEstimate struct {
	BaseFareCents     int     `json:"base_fare_cents"`
	DistanceFareCents int     `json:"distance_fare_cents"`
	TimeFareCents     int     `json:"time_fare_cents"`
	SubtotalCents     int     `json:"subtotal_cents"`
	SurgeMultiplier   float64 `json:"surge_multiplier"`
	TotalFareCents    int     `json:"total_fare_cents"`
	DistanceKm        float64 `json:"distance_km"`
	EstimatedMinutes  float64 `json:"estimated_minutes"`
	Demand            int     `json:"demand"`
	Supply            int     `json:"supply"`
	DemandSupplyRatio float64 `json:"demand_supply_ratio"`
}

// ─── PricingService ─────────────────────────────────────────

// PricingService calculates dynamic fares with surge pricing.
//
// Formula:
//   Price = (BaseFare + (Distance × PerKmRate) + (Time × PerMinRate)) × SurgeMultiplier
//
// Surge logic:
//   1. Query Redis (cache) or PostGIS (fallback) for demand/supply in the area.
//   2. Compute ratio R = Demand / Supply.
//   3. Apply tiered multiplier based on R.
type PricingService struct {
	repo   *repository.PricingRepository
	config FareConfig
}

// NewPricingService creates a pricing service with the given config.
func NewPricingService(repo *repository.PricingRepository, config FareConfig) *PricingService {
	return &PricingService{repo: repo, config: config}
}

// EstimateFare calculates the fare for a ride between origin and destination.
//
// Steps:
//  1. Calculate distance (Haversine) and estimated time.
//  2. Query demand/supply ratio for the origin area.
//  3. Determine surge multiplier.
//  4. Apply the pricing formula.
//
// Complexity: O(1) math + O(1) Redis lookup (or O(log N) PostGIS on cache miss).
func (s *PricingService) EstimateFare(
	ctx context.Context,
	origin model.Location,
	destination model.Location,
) (*FareEstimate, error) {

	// ── Step 1: Distance & Time ─────────────────────────
	distanceKm := geo.HaversineKm(origin, destination)
	estimatedMinutes := geo.EstimateTimeMinutes(origin, destination)

	log.Printf("[pricing] Route: %.2f km, ~%.1f min", distanceKm, estimatedMinutes)

	// ── Step 2: Demand/Supply for surge ─────────────────
	ds, err := s.repo.GetDemandSupply(ctx, origin, s.config.SurgeRadiusM)
	if err != nil {
		// On error, default to no surge (graceful degradation).
		log.Printf("[pricing] WARNING: demand/supply query failed: %v — defaulting to no surge", err)
		ds = &repository.DemandSupply{Demand: 0, Supply: 1, Ratio: 0}
	}

	log.Printf("[pricing] Demand=%d, Supply=%d, Ratio=%.2f", ds.Demand, ds.Supply, ds.Ratio)

	// ── Step 3: Surge multiplier ────────────────────────
	surge := calculateSurgeMultiplier(ds.Ratio)

	log.Printf("[pricing] Surge multiplier: %.1fx", surge)

	// ── Step 4: Fare formula ────────────────────────────
	//   Price = (BaseFare + Distance*Rate + Time*Rate) × Surge

	baseFare := s.config.BaseFareCents
	distanceFare := int(math.Round(distanceKm * float64(s.config.PerKmRateCents)))
	timeFare := int(math.Round(estimatedMinutes * float64(s.config.PerMinRateCents)))

	subtotal := baseFare + distanceFare + timeFare
	total := int(math.Round(float64(subtotal) * surge))

	// Apply minimum fare floor.
	if total < s.config.MinFareCents {
		total = s.config.MinFareCents
	}

	estimate := &FareEstimate{
		BaseFareCents:     baseFare,
		DistanceFareCents: distanceFare,
		TimeFareCents:     timeFare,
		SubtotalCents:     subtotal,
		SurgeMultiplier:   surge,
		TotalFareCents:    total,
		DistanceKm:        math.Round(distanceKm*100) / 100,
		EstimatedMinutes:  math.Round(estimatedMinutes*10) / 10,
		Demand:            ds.Demand,
		Supply:            ds.Supply,
		DemandSupplyRatio: math.Round(ds.Ratio*100) / 100,
	}

	log.Printf("[pricing] Fare: ₹%.2f (base=₹%.2f + dist=₹%.2f + time=₹%.2f) × %.1fx surge",
		float64(total)/100, float64(baseFare)/100,
		float64(distanceFare)/100, float64(timeFare)/100, surge)

	return estimate, nil
}

// ─── Surge Calculation ──────────────────────────────────────

// calculateSurgeMultiplier returns the surge multiplier for a given
// demand/supply ratio.
//
//	R ≤ 1.5  →  1.0x  (normal pricing)
//	R > 1.5  →  1.2x  (moderate surge)
//	R > 2.0  →  1.5x  (high surge)
func calculateSurgeMultiplier(ratio float64) float64 {
	switch {
	case ratio > SurgeThresholdHigh:
		return SurgeMultiplierHigh
	case ratio > SurgeThresholdModerate:
		return SurgeMultiplierModerate
	default:
		return SurgeMultiplierNone
	}
}
