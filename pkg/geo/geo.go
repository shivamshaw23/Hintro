// Package geo provides geographic utility functions for ride pooling.
//
// All distance calculations use the Haversine formula on WGS-84 coordinates.
// Travel time is estimated using a constant average speed — suitable for
// assignment/demo purposes. In production, swap with OSRM or Google Maps API.
package geo

import (
	"math"

	"github.com/shiva/hintro/internal/model"
)

// ─── Constants ──────────────────────────────────────────────

const (
	// EarthRadiusKm is the mean radius of Earth in kilometers.
	EarthRadiusKm = 6371.0

	// EarthRadiusM is the mean radius of Earth in meters.
	EarthRadiusM = 6_371_000.0

	// AverageSpeedKmph is the assumed average city driving speed.
	// Used for time estimation when a routing engine is not available.
	AverageSpeedKmph = 30.0
)

// ─── Distance ───────────────────────────────────────────────

// HaversineKm returns the great-circle distance between two points in kilometers.
//
// Complexity: O(1)
func HaversineKm(a, b model.Location) float64 {
	dLat := degToRad(b.Lat - a.Lat)
	dLon := degToRad(b.Lon - a.Lon)

	sinLat := math.Sin(dLat / 2)
	sinLon := math.Sin(dLon / 2)

	h := sinLat*sinLat +
		math.Cos(degToRad(a.Lat))*math.Cos(degToRad(b.Lat))*sinLon*sinLon

	return 2 * EarthRadiusKm * math.Asin(math.Sqrt(h))
}

// HaversineM returns the great-circle distance between two points in meters.
func HaversineM(a, b model.Location) float64 {
	return HaversineKm(a, b) * 1000.0
}

// ─── Route Calculations ─────────────────────────────────────

// RouteDistanceKm returns the total distance of an ordered route in kilometers.
//
// Complexity: O(S) where S = number of stops.
func RouteDistanceKm(route []model.Location) float64 {
	total := 0.0
	for i := 0; i < len(route)-1; i++ {
		total += HaversineKm(route[i], route[i+1])
	}
	return total
}

// RouteTimeMinutes returns the estimated travel time for a route in minutes,
// assuming AverageSpeedKmph.
//
// Complexity: O(S)
func RouteTimeMinutes(route []model.Location) float64 {
	return (RouteDistanceKm(route) / AverageSpeedKmph) * 60.0
}

// EstimateTimeMinutes returns the estimated direct travel time between two
// points in minutes.
//
// Complexity: O(1)
func EstimateTimeMinutes(a, b model.Location) float64 {
	return (HaversineKm(a, b) / AverageSpeedKmph) * 60.0
}

// ─── Route Manipulation ────────────────────────────────────

// InsertStop returns a new route with the given stop inserted at the specified
// index. The original route is NOT modified.
//
// Complexity: O(S)
func InsertStop(route []model.Location, index int, stop model.Location) []model.Location {
	newRoute := make([]model.Location, 0, len(route)+1)
	newRoute = append(newRoute, route[:index]...)
	newRoute = append(newRoute, stop)
	newRoute = append(newRoute, route[index:]...)
	return newRoute
}

// FindBestInsertionIndex finds the index in the route where inserting the
// new stop causes the LEAST increase in total route time.
// Returns (bestIndex, addedTimeMinutes).
//
// For airport pooling (all heading to same destination), the last stop in
// the route is the airport. We try every insertion point before it.
//
// Complexity: O(S²) — but S ≤ 6 in practice, so effectively constant.
func FindBestInsertionIndex(route []model.Location, stop model.Location) (int, float64) {
	if len(route) < 2 {
		return 0, 0
	}

	currentTime := RouteTimeMinutes(route)
	bestIdx := 0
	bestAdded := math.MaxFloat64

	// Try inserting at every position except after the last stop (airport).
	for i := 0; i < len(route); i++ {
		candidate := InsertStop(route, i, stop)
		added := RouteTimeMinutes(candidate) - currentTime
		if added < bestAdded {
			bestAdded = added
			bestIdx = i
		}
	}

	return bestIdx, bestAdded
}

// ─── Helpers ────────────────────────────────────────────────

func degToRad(deg float64) float64 {
	return deg * (math.Pi / 180.0)
}
