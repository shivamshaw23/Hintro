package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/shiva/hintro/internal/repository"
)

// ─── Booking Errors ─────────────────────────────────────────

var (
	// ErrCabFull is returned when the cab has no remaining capacity.
	ErrCabFull = errors.New("cab is full: no remaining seats or luggage capacity")

	// ErrBookingTimeout is returned when the transaction lock wait exceeds
	// the context deadline (another transaction held the lock too long).
	ErrBookingTimeout = errors.New("booking timed out waiting for lock")

	// ErrRequestNotPending is returned when the ride request is not in 'pending' state.
	ErrRequestNotPending = errors.New("ride request is not in pending state")

	// ErrCabNotAvailable is returned when the cab is offline or already on a trip.
	ErrCabNotAvailable = errors.New("cab is not available for booking")

	// ErrNoCabNearby is returned when no available cab is found near the pickup.
	ErrNoCabNearby = errors.New("no available cab found nearby")
)

// ─── BookingService ─────────────────────────────────────────

// BookingService handles ride bookings with strict concurrency control.
//
// Concurrency model:
//   - Uses PostgreSQL SELECT ... FOR UPDATE (pessimistic locking).
//   - The cab row is locked for the duration of the transaction.
//   - Concurrent bookings for the same cab will serialize automatically.
//   - A 5-second context timeout prevents deadlock starvation.
type BookingService struct {
	bookingRepo  *repository.BookingRepository
	matchingSvc  *MatchingService
}

// NewBookingService creates a booking service.
func NewBookingService(
	bookingRepo *repository.BookingRepository,
	matchingSvc *MatchingService,
) *BookingService {
	return &BookingService{
		bookingRepo:  bookingRepo,
		matchingSvc:  matchingSvc,
	}
}

// BookRide is the main booking entry point.
//
// Flow:
//  1. Run the matching algorithm to find a compatible trip.
//  2. If no match, find a nearby available cab and create a new trip.
//  3. Execute the booking transaction with pessimistic row locking.
//  4. Handle race conditions: if the cab fills up between match and book,
//     return ErrCabFull.
//
// Concurrency guarantee:
//   Two users booking the last seat at the same millisecond:
//     User A: gets the lock → books seat → commits (success)
//     User B: blocks on lock → re-reads → no seats left → rollback (ErrCabFull)
func (s *BookingService) BookRide(ctx context.Context, requestID int64) (*repository.BookingResult, error) {
	log.Printf("[booking] Starting booking for request #%d", requestID)

	// ── Step 1: Try to match to an existing trip ────────
	var tripID, cabID int64

	matchResult, err := s.matchingSvc.MatchRiders(ctx, requestID)
	if err == nil {
		// Match found — use this trip.
		tripID = matchResult.TripID
		cabID = matchResult.CabID
		log.Printf("[booking] Matched to existing trip #%d (cab #%d)", tripID, cabID)
	} else if errors.Is(err, ErrNoMatch) {
		// No match — create a new trip.
		log.Printf("[booking] No existing match; creating new trip")

		newTrip, err := s.createNewTrip(ctx, requestID)
		if err != nil {
			return nil, err
		}
		tripID = newTrip.tripID
		cabID = newTrip.cabID
		log.Printf("[booking] Created new trip #%d (cab #%d)", tripID, cabID)
	} else {
		// Other errors (not found, already matched, etc.)
		return nil, s.classifyError(err)
	}

	// ── Step 2: Execute the booking transaction ─────────
	// This is where the pessimistic lock kicks in.
	// Create a deadline context for the transaction.
	txCtx, cancel := context.WithTimeout(ctx, repository.DefaultBookingTimeout)
	defer cancel()

	result, err := s.bookingRepo.BookRide(txCtx, requestID, cabID, tripID)
	if err != nil {
		return nil, s.classifyError(err)
	}

	log.Printf("[booking] ✓ Booked request #%d into trip #%d (cab #%d) — %d seats remaining",
		result.RequestID, result.TripID, result.CabID, result.RemainingSeats)

	return result, nil
}

// ─── Private helpers ────────────────────────────────────────

type newTripResult struct {
	tripID int64
	cabID  int64
}

// createNewTrip finds an available cab and creates a new trip for the request.
func (s *BookingService) createNewTrip(ctx context.Context, requestID int64) (*newTripResult, error) {
	// Fetch the request to get origin and direction.
	req, err := s.matchingSvc.Repo.GetRideRequest(ctx, requestID, false)
	if err != nil {
		return nil, fmt.Errorf("booking: fetch request: %w", err)
	}

	// Find nearest available cab (within 10km).
	cab, err := s.bookingRepo.FindAvailableCabNear(ctx, req.Origin, 10000)
	if err != nil {
		return nil, ErrNoCabNearby
	}

	// Create a new trip on this cab.
	tripID, err := s.bookingRepo.CreateTrip(ctx, cab.ID, req.Direction)
	if err != nil {
		return nil, fmt.Errorf("booking: create trip: %w", err)
	}

	return &newTripResult{tripID: tripID, cabID: cab.ID}, nil
}

// classifyError maps low-level DB/service errors to user-facing booking errors.
func (s *BookingService) classifyError(err error) error {
	if err == nil {
		return nil
	}

	errMsg := err.Error()

	// Context timeout → lock wait exceeded
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ErrBookingTimeout
	}

	// Capacity errors
	if strings.Contains(errMsg, "seats remaining") {
		return ErrCabFull
	}
	if strings.Contains(errMsg, "luggage slots remaining") {
		return ErrCabFull
	}

	// Status errors
	if strings.Contains(errMsg, "expected 'pending'") ||
		errors.Is(err, ErrAlreadyMatched) {
		return ErrRequestNotPending
	}
	if strings.Contains(errMsg, "not bookable") || strings.Contains(errMsg, "not available") {
		return ErrCabNotAvailable
	}

	// Request not found
	if errors.Is(err, ErrRequestNotFound) {
		return ErrRequestNotFound
	}

	return fmt.Errorf("booking: unexpected error: %w", err)
}
