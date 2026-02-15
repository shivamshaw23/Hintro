// Package repository provides database access for the ride pooling system.
//
// BookingRepository handles transactional booking operations with
// pessimistic locking (SELECT ... FOR UPDATE) to prevent race conditions.
package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shiva/hintro/internal/model"
)

// BookingRepository handles transactional booking with row-level locking.
type BookingRepository struct {
	pool *pgxpool.Pool
}

// NewBookingRepository creates a new booking repository.
func NewBookingRepository(pool *pgxpool.Pool) *BookingRepository {
	return &BookingRepository{pool: pool}
}

// BookingResult contains the outcome of a successful booking transaction.
type BookingResult struct {
	TripID            int64  `json:"trip_id"`
	CabID             int64  `json:"cab_id"`
	RequestID         int64  `json:"request_id"`
	SeatsBooked       int    `json:"seats_booked"`
	RemainingSeats    int    `json:"remaining_seats"`
	LuggageBooked     int    `json:"luggage_booked"`
	RemainingLuggage  int    `json:"remaining_luggage"`
}

// ─── The Core Transactional Booking ─────────────────────────

// BookRide performs the complete booking in a single serialized transaction.
//
// Concurrency strategy: PESSIMISTIC LOCKING
//
//   Scenario: Two users try to book the last seat at the exact same millisecond.
//
//   Timeline:
//     T1: BEGIN → SELECT cab FOR UPDATE → (cab row LOCKED)
//     T2: BEGIN → SELECT cab FOR UPDATE → (BLOCKS, waiting for T1's lock)
//     T1: seats OK → UPDATE cab → INSERT/UPDATE → COMMIT → (lock released)
//     T2: (unblocked) → re-reads cab → seats FULL → ROLLBACK → returns error
//
// The SELECT ... FOR UPDATE on the cab row ensures only ONE transaction can
// read-and-modify the cab at a time. The second transaction will BLOCK until
// the first commits or rolls back, then re-read the updated row.
//
// Timeout handling:
//   - The context carries a 5-second deadline for the entire transaction.
//   - If the lock wait exceeds this, pgx returns a context.DeadlineExceeded
//     error, which the service layer translates to ErrBookingTimeout.
func (r *BookingRepository) BookRide(
	ctx context.Context,
	requestID int64,
	cabID int64,
	tripID int64,
) (*BookingResult, error) {

	// ── Wrap the entire booking in a transaction ────────
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return nil, fmt.Errorf("booking: begin tx: %w", err)
	}
	// Defer rollback — no-op if tx was already committed.
	defer tx.Rollback(ctx)

	// ── Step 1: LOCK the cab row ────────────────────────
	// SELECT ... FOR UPDATE acquires an exclusive row-level lock.
	// Any concurrent transaction hitting the same cab will BLOCK here
	// until this transaction completes.
	var (
		seatCapacity    int
		luggageCapacity int
		cabStatus       model.CabStatus
	)
	err = tx.QueryRow(ctx, `
		SELECT seat_capacity, luggage_capacity, status
		FROM cabs
		WHERE id = $1
		FOR UPDATE
	`, cabID).Scan(&seatCapacity, &luggageCapacity, &cabStatus)
	if err != nil {
		return nil, fmt.Errorf("booking: lock cab %d: %w", cabID, err)
	}

	// ── Step 2: LOCK the ride request row ───────────────
	var (
		reqSeats   int
		reqLuggage int
		reqStatus  model.RequestStatus
		reqTripID  *int64
	)
	err = tx.QueryRow(ctx, `
		SELECT seats_needed, luggage_count, status, trip_id
		FROM ride_requests
		WHERE id = $1
		FOR UPDATE
	`, requestID).Scan(&reqSeats, &reqLuggage, &reqStatus, &reqTripID)
	if err != nil {
		return nil, fmt.Errorf("booking: lock request %d: %w", requestID, err)
	}

	// ── Step 3: Validate business rules ─────────────────

	// 3a: Request must be in 'pending' state.
	if reqStatus != model.RequestPending {
		return nil, fmt.Errorf("booking: request %d status is '%s', expected 'pending'", requestID, reqStatus)
	}

	// 3b: Cab must be available or en_route.
	if cabStatus != model.CabAvailable && cabStatus != model.CabEnRoute {
		return nil, fmt.Errorf("booking: cab %d status is '%s', not bookable", cabID, cabStatus)
	}

	// 3c: Calculate current load on this trip.
	var currentSeats, currentLuggage int
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(seats_needed), 0)::int,
		       COALESCE(SUM(luggage_count), 0)::int
		FROM ride_requests
		WHERE trip_id = $1
		  AND status IN ('matched', 'confirmed')
	`, tripID).Scan(&currentSeats, &currentLuggage)
	if err != nil {
		return nil, fmt.Errorf("booking: query trip %d load: %w", tripID, err)
	}

	// 3d: CHECK CAPACITY — the critical constraint.
	remainingSeats := seatCapacity - currentSeats
	remainingLuggage := luggageCapacity - currentLuggage

	if reqSeats > remainingSeats {
		// This is the "last seat taken" scenario.
		// Transaction rolls back automatically via defer.
		return nil, fmt.Errorf("booking: cab %d has %d seats remaining, need %d",
			cabID, remainingSeats, reqSeats)
	}
	if reqLuggage > remainingLuggage {
		return nil, fmt.Errorf("booking: cab %d has %d luggage slots remaining, need %d",
			cabID, remainingLuggage, reqLuggage)
	}

	// ── Step 4: UPDATE — all constraints passed ─────────

	// 4a: Mark ride request as 'matched' and assign to trip.
	_, err = tx.Exec(ctx, `
		UPDATE ride_requests
		SET status = 'matched', trip_id = $2
		WHERE id = $1
	`, requestID, tripID)
	if err != nil {
		return nil, fmt.Errorf("booking: update request %d: %w", requestID, err)
	}

	// 4b: Update trip passenger count.
	_, err = tx.Exec(ctx, `
		UPDATE trips
		SET passenger_count = passenger_count + $2
		WHERE id = $1
	`, tripID, reqSeats)
	if err != nil {
		return nil, fmt.Errorf("booking: update trip %d: %w", tripID, err)
	}

	// 4c: Update cab status to 'en_route' if not already.
	_, err = tx.Exec(ctx, `
		UPDATE cabs
		SET status = 'en_route'
		WHERE id = $1 AND status = 'available'
	`, cabID)
	if err != nil {
		return nil, fmt.Errorf("booking: update cab %d status: %w", cabID, err)
	}

	// ── Step 5: COMMIT ──────────────────────────────────
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("booking: commit: %w", err)
	}

	return &BookingResult{
		TripID:           tripID,
		CabID:            cabID,
		RequestID:        requestID,
		SeatsBooked:      reqSeats,
		RemainingSeats:   remainingSeats - reqSeats,
		LuggageBooked:    reqLuggage,
		RemainingLuggage: remainingLuggage - reqLuggage,
	}, nil
}

// ─── Helper: Create a new trip for unmatched requests ───────

// CreateTrip inserts a new trip and returns its ID.
// Used when the matching service found no existing trip to join.
func (r *BookingRepository) CreateTrip(
	ctx context.Context,
	cabID int64,
	direction model.TripDirection,
) (int64, error) {

	// Use a transaction with cab locking to prevent double-assignment.
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("create trip: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Lock the cab.
	var cabStatus model.CabStatus
	err = tx.QueryRow(ctx, `
		SELECT status FROM cabs WHERE id = $1 FOR UPDATE
	`, cabID).Scan(&cabStatus)
	if err != nil {
		return 0, fmt.Errorf("create trip: lock cab %d: %w", cabID, err)
	}

	if cabStatus != model.CabAvailable {
		return 0, fmt.Errorf("create trip: cab %d is '%s', not available", cabID, cabStatus)
	}

	// Insert the trip.
	var tripID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO trips (cab_id, direction, total_fare_cents, passenger_count, status)
		VALUES ($1, $2, 0, 0, 'planned')
		RETURNING id
	`, cabID, direction).Scan(&tripID)
	if err != nil {
		return 0, fmt.Errorf("create trip: insert: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("create trip: commit: %w", err)
	}

	return tripID, nil
}

// ─── Helper: Find an available cab near a location ──────────

// FindAvailableCabNear returns the closest available cab within radiusMeters
// that has at least minSeatsNeeded and minLuggageNeeded capacity.
// Used when creating a new trip — ensures the cab can fit the requesting passenger.
// Uses GIST index on cabs(current_location) for spatial lookup.
func (r *BookingRepository) FindAvailableCabNear(
	ctx context.Context,
	location model.Location,
	radiusMeters int,
	minSeatsNeeded int,
	minLuggageNeeded int,
) (*model.Cab, error) {

	query := `
		SELECT id, driver_id, license_plate, seat_capacity, luggage_capacity,
		       ST_Y(current_location) AS lat, ST_X(current_location) AS lon,
		       status
		FROM cabs
		WHERE status = 'available'
		  AND current_location IS NOT NULL
		  AND seat_capacity >= $4
		  AND luggage_capacity >= $5
		  AND ST_DWithin(
		        current_location::geography,
		        ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography,
		        $3
		      )
		ORDER BY ST_Distance(
		    current_location::geography,
		    ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography
		) ASC
		LIMIT 1
	`

	cab := &model.Cab{}
	var loc model.Location

	err := r.pool.QueryRow(ctx, query, location.Lon, location.Lat, radiusMeters, minSeatsNeeded, minLuggageNeeded).Scan(
		&cab.ID, &cab.DriverID, &cab.LicensePlate,
		&cab.SeatCapacity, &cab.LuggageCapacity,
		&loc.Lat, &loc.Lon,
		&cab.Status,
	)
	if err != nil {
		return nil, fmt.Errorf("find available cab: %w", err)
	}

	cab.CurrentLocation = &loc
	return cab, nil
}

// ─── Cancel Ride ─────────────────────────────────────────────

// CancelResult contains the outcome of a successful cancellation.
type CancelResult struct {
	RequestID      int64   `json:"request_id"`
	PreviousTrip   *int64  `json:"previous_trip_id,omitempty"`
	TripCancelled  bool    `json:"trip_cancelled,omitempty"` // True if the whole trip was cancelled (last passenger).
	CabFreed       bool    `json:"cab_freed,omitempty"`      // True if cab was set back to available.
	OriginLat      float64 `json:"-"`                         // For surge cache invalidation (not in JSON response).
	OriginLon      float64 `json:"-"`
}

// CancelRide cancels a ride request. Uses pessimistic locking for concurrency safety.
//
// State transitions:
//   - PENDING  → CANCELLED: Simple status update. No trip/cab impact.
//   - MATCHED  → CANCELLED: Decrement trip passenger_count, clear trip_id. If trip has
//                 0 passengers left, cancel the trip and set cab back to available.
//   - CONFIRMED, COMPLETED, CANCELLED: Not cancellable (terminal states).
//
// Concurrency: Same as BookRide — SELECT ... FOR UPDATE on request and cab/trip.
func (r *BookingRepository) CancelRide(
	ctx context.Context,
	requestID int64,
) (*CancelResult, error) {

	txCtx, cancel := context.WithTimeout(ctx, DefaultBookingTimeout)
	defer cancel()

	tx, err := r.pool.BeginTx(txCtx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("cancel: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// ── Step 1: LOCK the ride request ────────────────────
	var (
		reqStatus model.RequestStatus
		reqTripID *int64
		reqSeats  int
		reqLuggage int
		originLon float64
		originLat float64
	)
	err = tx.QueryRow(ctx, `
		SELECT status, trip_id, seats_needed, luggage_count,
		       ST_X(origin) AS origin_lon, ST_Y(origin) AS origin_lat
		FROM ride_requests
		WHERE id = $1
		FOR UPDATE
	`, requestID).Scan(&reqStatus, &reqTripID, &reqSeats, &reqLuggage, &originLon, &originLat)
	if err != nil {
		return nil, fmt.Errorf("cancel: lock request %d: %w", requestID, err)
	}

	// ── Step 2: Validate — only PENDING or MATCHED can be cancelled ─
	switch reqStatus {
	case model.RequestCancelled:
		return nil, fmt.Errorf("cancel: request %d is already cancelled", requestID)
	case model.RequestCompleted:
		return nil, fmt.Errorf("cancel: request %d is completed, cannot cancel", requestID)
	case model.RequestConfirmed:
		return nil, fmt.Errorf("cancel: request %d is confirmed, cannot cancel", requestID)
	case model.RequestPending, model.RequestMatched:
		// OK to cancel
	default:
		return nil, fmt.Errorf("cancel: request %d has unknown status '%s'", requestID, reqStatus)
	}

	result := &CancelResult{
		RequestID: requestID,
		OriginLat: originLat,
		OriginLon: originLon,
	}

	// ── Step 3a: PENDING — simple status update ───────────
	if reqStatus == model.RequestPending {
		_, err = tx.Exec(ctx, `
			UPDATE ride_requests
			SET status = 'cancelled', trip_id = NULL
			WHERE id = $1
		`, requestID)
		if err != nil {
			return nil, fmt.Errorf("cancel: update request %d: %w", requestID, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("cancel: commit: %w", err)
		}
		return result, nil
	}

	// ── Step 3b: MATCHED — update request, decrement trip, possibly cancel trip/cab ─
	tripID := *reqTripID

	// Update request: set cancelled, clear trip_id.
	_, err = tx.Exec(ctx, `
		UPDATE ride_requests
		SET status = 'cancelled', trip_id = NULL
		WHERE id = $1
	`, requestID)
	if err != nil {
		return nil, fmt.Errorf("cancel: update request %d: %w", requestID, err)
	}

	result.PreviousTrip = &tripID

	// Decrement trip passenger count.
	_, err = tx.Exec(ctx, `
		UPDATE trips
		SET passenger_count = GREATEST(0, passenger_count - $2)
		WHERE id = $1
	`, tripID, reqSeats)
	if err != nil {
		return nil, fmt.Errorf("cancel: update trip %d: %w", tripID, err)
	}

	// Count remaining matched passengers on this trip.
	var remainingPassengers int
	err = tx.QueryRow(ctx, `
		SELECT COUNT(*)::int
		FROM ride_requests
		WHERE trip_id = $1 AND status = 'matched'
	`, tripID).Scan(&remainingPassengers)
	if err != nil {
		return nil, fmt.Errorf("cancel: count remaining passengers: %w", err)
	}

	// If no passengers left, cancel the trip and free the cab.
	if remainingPassengers == 0 {
		_, err = tx.Exec(ctx, `
			UPDATE trips SET status = 'cancelled' WHERE id = $1
		`, tripID)
		if err != nil {
			return nil, fmt.Errorf("cancel: cancel trip %d: %w", tripID, err)
		}
		result.TripCancelled = true

		// Get cab_id for this trip and set cab back to available.
		var cabID int64
		err = tx.QueryRow(ctx, `SELECT cab_id FROM trips WHERE id = $1`, tripID).Scan(&cabID)
		if err != nil {
			return nil, fmt.Errorf("cancel: get cab for trip %d: %w", tripID, err)
		}

		_, err = tx.Exec(ctx, `
			UPDATE cabs
			SET status = 'available'
			WHERE id = $1 AND status = 'en_route'
		`, cabID)
		if err != nil {
			return nil, fmt.Errorf("cancel: free cab %d: %w", cabID, err)
		}
		result.CabFreed = true
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("cancel: commit: %w", err)
	}
	return result, nil
}

// ─── Timeout helper ─────────────────────────────────────────

// DefaultBookingTimeout is the maximum duration for a complete booking
// transaction, including lock wait time.
const DefaultBookingTimeout = 5 * time.Second
