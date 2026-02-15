package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/shiva/hintro/internal/model"
	"github.com/shiva/hintro/internal/repository"
)

// ─── Cancel Errors ─────────────────────────────────────────

var (
	ErrCannotCancel   = errors.New("ride request cannot be cancelled")
	ErrAlreadyCancelled = errors.New("ride request is already cancelled")
)

// ─── CancelService ─────────────────────────────────────────

// CancelService handles ride cancellations with proper state transitions
// and integration with matching/booking (frees capacity) and pricing (invalidates surge cache).
type CancelService struct {
	bookingRepo *repository.BookingRepository
	pricingRepo *repository.PricingRepository
}

// NewCancelService creates a cancel service.
func NewCancelService(
	bookingRepo *repository.BookingRepository,
	pricingRepo *repository.PricingRepository,
) *CancelService {
	return &CancelService{
		bookingRepo: bookingRepo,
		pricingRepo: pricingRepo,
	}
}

// CancelRide cancels a ride request.
//
// State transitions:
//   - PENDING  → CANCELLED: Request marked cancelled. No trip/cab impact.
//     Matching: Request no longer appears in pending pool.
//   - MATCHED  → CANCELLED: Decrement trip passenger_count, clear trip_id.
//     If last passenger: cancel trip, set cab back to available.
//     Matching: Trip becomes available for new bookings (or disappears if cancelled).
//   - CONFIRMED, COMPLETED, CANCELLED: Returns ErrCannotCancel.
//
// Integration:
//   - Invalidates surge cache for the request's origin area (demand/supply changed).
func (s *CancelService) CancelRide(ctx context.Context, requestID int64) (*repository.CancelResult, error) {
	log.Printf("[cancel] Processing cancellation for request #%d", requestID)

	result, err := s.bookingRepo.CancelRide(ctx, requestID)
	if err != nil {
		return nil, s.classifyError(err)
	}

	// Invalidate surge cache for the origin area — demand/supply has changed.
	// PENDING→cancelled: demand decreased. MATCHED→cancelled: supply may have increased (cab freed).
	s.pricingRepo.InvalidateSurgeCache(ctx, model.Location{
		Lat: result.OriginLat,
		Lon: result.OriginLon,
	})
	log.Printf("[cancel] Invalidated surge cache for origin (%.4f, %.4f)", result.OriginLat, result.OriginLon)

	log.Printf("[cancel] ✓ Cancelled request #%d (trip_cancelled=%v, cab_freed=%v)",
		requestID, result.TripCancelled, result.CabFreed)

	return result, nil
}

func (s *CancelService) classifyError(err error) error {
	if err == nil {
		return nil
	}
	errMsg := err.Error()
	if strings.Contains(errMsg, "already cancelled") {
		return ErrAlreadyCancelled
	}
	if strings.Contains(errMsg, "cannot cancel") || strings.Contains(errMsg, "completed") || strings.Contains(errMsg, "confirmed") {
		return ErrCannotCancel
	}
	if strings.Contains(errMsg, "not found") || strings.Contains(errMsg, "no rows") {
		return ErrRequestNotFound
	}
	return fmt.Errorf("cancel: %w", err)
}
