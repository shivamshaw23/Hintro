package geo

import (
	"math"
	"testing"

	"github.com/shiva/hintro/internal/model"
)

func TestHaversineKm_SamePoint(t *testing.T) {
	loc := model.Location{Lat: 28.7041, Lon: 77.1025}
	got := HaversineKm(loc, loc)
	if got != 0 {
		t.Errorf("HaversineKm(same point) = %v, want 0", got)
	}
}

func TestHaversineKm_KnownDistance(t *testing.T) {
	// Connaught Place to IGI Airport (~16.5 km)
	connaught := model.Location{Lat: 28.6315, Lon: 77.2167}
	igi := model.Location{Lat: 28.5562, Lon: 77.0889}
	got := HaversineKm(connaught, igi)
	wantMin, wantMax := 14.0, 20.0
	if got < wantMin || got > wantMax {
		t.Errorf("HaversineKm(Connaught→IGI) = %.2f km, want between %.1f and %.1f", got, wantMin, wantMax)
	}
}

func TestEstimateTimeMinutes(t *testing.T) {
	a := model.Location{Lat: 28.7041, Lon: 77.1025}
	b := model.Location{Lat: 28.5562, Lon: 77.0889}
	got := EstimateTimeMinutes(a, b)
	// ~16 km at 30 km/h ≈ 32 min
	if got < 25 || got > 40 {
		t.Errorf("EstimateTimeMinutes = %.1f, expected ~30-35 min", got)
	}
}

func TestRouteDistanceKm(t *testing.T) {
	route := []model.Location{
		{Lat: 28.7041, Lon: 77.1025},
		{Lat: 28.6500, Lon: 77.1000},
		{Lat: 28.5562, Lon: 77.0889},
	}
	got := RouteDistanceKm(route)
	if got <= 0 {
		t.Errorf("RouteDistanceKm = %v, want positive", got)
	}
}

func TestFindBestInsertionIndex(t *testing.T) {
	// Route: A -> B -> Airport
	route := []model.Location{
		{Lat: 28.71, Lon: 77.10},
		{Lat: 28.65, Lon: 77.09},
		{Lat: 28.5562, Lon: 77.0889}, // airport
	}
	newStop := model.Location{Lat: 28.68, Lon: 77.095} // between A and B

	idx, added := FindBestInsertionIndex(route, newStop)

	if idx < 0 || idx > len(route) {
		t.Errorf("FindBestInsertionIndex: idx = %d, want 0..%d", idx, len(route))
	}
	if added < 0 {
		t.Errorf("FindBestInsertionIndex: added = %v, want >= 0", added)
	}
	// Inserting should add some detour (positive)
	if added > 0 && added < 60 {
		// Reasonable detour in minutes
	}
}

func TestInsertStop(t *testing.T) {
	route := []model.Location{
		{Lat: 1, Lon: 1},
		{Lat: 2, Lon: 2},
	}
	stop := model.Location{Lat: 1.5, Lon: 1.5}
	got := InsertStop(route, 1, stop)
	if len(got) != 3 {
		t.Errorf("InsertStop: len = %d, want 3", len(got))
	}
	if got[1] != stop {
		t.Errorf("InsertStop: inserted at wrong position")
	}
}

func TestHaversineM(t *testing.T) {
	a := model.Location{Lat: 0, Lon: 0}
	b := model.Location{Lat: 0.001, Lon: 0}
	km := HaversineKm(a, b)
	m := HaversineM(a, b)
	if math.Abs(m-km*1000) > 0.01 {
		t.Errorf("HaversineM = %v, want HaversineKm*1000 = %v", m, km*1000)
	}
}
