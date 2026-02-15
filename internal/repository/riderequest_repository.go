package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shiva/hintro/internal/model"
)

// RideRequestRepository handles CRUD + cancellation for ride requests.
type RideRequestRepository struct {
	pool *pgxpool.Pool
}

// NewRideRequestRepository creates a new repository.
func NewRideRequestRepository(pool *pgxpool.Pool) *RideRequestRepository {
	return &RideRequestRepository{pool: pool}
}

// CreateRideRequest inserts a new pending ride request.
// Enforces luggage constraints: LuggageCount must be in [0, 8] (matches DB CHECK).
func (r *RideRequestRepository) CreateRideRequest(
	ctx context.Context,
	req *model.RideRequest,
) (*model.RideRequest, error) {
	if req.LuggageCount < model.MinLuggagePerRequest || req.LuggageCount > model.MaxLuggagePerRequest {
		return nil, fmt.Errorf("create ride request: luggage_count must be between %d and %d, got %d",
			model.MinLuggagePerRequest, model.MaxLuggagePerRequest, req.LuggageCount)
	}
	query := `
		INSERT INTO ride_requests (
			user_id, origin, destination, direction,
			seats_needed, luggage_count, tolerance_meters,
			status, scheduled_at
		) VALUES (
			$1,
			ST_SetSRID(ST_MakePoint($2, $3), 4326),
			ST_SetSRID(ST_MakePoint($4, $5), 4326),
			$6, $7, $8, $9, 'pending', $10
		)
		RETURNING id, created_at, updated_at
	`
	err := r.pool.QueryRow(ctx, query,
		req.UserID,
		req.Origin.Lon, req.Origin.Lat,
		req.Destination.Lon, req.Destination.Lat,
		req.Direction,
		req.SeatsNeeded, req.LuggageCount, req.ToleranceMeters,
		req.ScheduledAt,
	).Scan(&req.ID, &req.CreatedAt, &req.UpdatedAt)

	if err != nil {
		return nil, fmt.Errorf("create ride request: %w", err)
	}

	req.Status = model.RequestPending
	return req, nil
}

// GetRideRequestByID fetches a ride request with full details.
func (r *RideRequestRepository) GetRideRequestByID(
	ctx context.Context, id int64,
) (*model.RideRequest, error) {
	query := `
		SELECT id, user_id,
		       ST_Y(origin) AS origin_lat, ST_X(origin) AS origin_lon,
		       ST_Y(destination) AS dest_lat, ST_X(destination) AS dest_lon,
		       direction, seats_needed, luggage_count, tolerance_meters,
		       status, trip_id, scheduled_at, created_at, updated_at
		FROM ride_requests
		WHERE id = $1
	`
	rr := &model.RideRequest{}
	var tripID *int64
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&rr.ID, &rr.UserID,
		&rr.Origin.Lat, &rr.Origin.Lon,
		&rr.Destination.Lat, &rr.Destination.Lon,
		&rr.Direction, &rr.SeatsNeeded, &rr.LuggageCount, &rr.ToleranceMeters,
		&rr.Status, &tripID, &rr.ScheduledAt, &rr.CreatedAt, &rr.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get ride request %d: %w", id, err)
	}
	rr.TripID = tripID
	return rr, nil
}

// CancelRideRequest cancels a ride and releases the seat back to the cab.
//
// Concurrency: Uses SELECT ... FOR UPDATE on both the ride_request and the
// trip/cab to prevent race conditions during cancellation.
//
// Flow:
//  1. Lock the ride request row.
//  2. If status is 'matched' → also lock the trip, decrement passenger_count.
//  3. Set ride_request status to 'cancelled', clear trip_id.
//  4. Commit atomically.
func (r *RideRequestRepository) CancelRideRequest(
	ctx context.Context, requestID int64,
) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return fmt.Errorf("cancel: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Step 1: Lock the ride request.
	var status model.RequestStatus
	var tripID *int64
	var seatsNeeded int
	err = tx.QueryRow(ctx, `
		SELECT status, trip_id, seats_needed
		FROM ride_requests
		WHERE id = $1
		FOR UPDATE
	`, requestID).Scan(&status, &tripID, &seatsNeeded)
	if err != nil {
		return fmt.Errorf("cancel: lock request %d: %w", requestID, err)
	}

	// Can only cancel pending or matched requests.
	if status != model.RequestPending && status != model.RequestMatched {
		return fmt.Errorf("cancel: request %d has status '%s', cannot cancel", requestID, status)
	}

	// Step 2: If matched to a trip, release the seat.
	if tripID != nil && status == model.RequestMatched {
		// Lock the trip and decrement.
		_, err = tx.Exec(ctx, `
			UPDATE trips
			SET passenger_count = GREATEST(passenger_count - $2, 0)
			WHERE id = $1
		`, *tripID, seatsNeeded)
		if err != nil {
			return fmt.Errorf("cancel: release seat on trip %d: %w", *tripID, err)
		}
	}

	// Step 3: Cancel the request.
	_, err = tx.Exec(ctx, `
		UPDATE ride_requests
		SET status = 'cancelled', trip_id = NULL
		WHERE id = $1
	`, requestID)
	if err != nil {
		return fmt.Errorf("cancel: update request %d: %w", requestID, err)
	}

	// Step 4: Commit.
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("cancel: commit: %w", err)
	}

	return nil
}

// GetTripByID fetches a trip with its passenger list.
func (r *RideRequestRepository) GetTripByID(
	ctx context.Context, tripID int64,
) (*model.Trip, []model.RideRequest, error) {
	// Fetch trip.
	trip := &model.Trip{}
	err := r.pool.QueryRow(ctx, `
		SELECT id, cab_id, direction, total_fare_cents, passenger_count,
		       status, started_at, completed_at, created_at, updated_at
		FROM trips WHERE id = $1
	`, tripID).Scan(
		&trip.ID, &trip.CabID, &trip.Direction,
		&trip.TotalFareCents, &trip.PassengerCount,
		&trip.Status, &trip.StartedAt, &trip.CompletedAt,
		&trip.CreatedAt, &trip.UpdatedAt,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("get trip %d: %w", tripID, err)
	}

	// Fetch passengers.
	rows, err := r.pool.Query(ctx, `
		SELECT id, user_id,
		       ST_Y(origin) AS lat, ST_X(origin) AS lon,
		       ST_Y(destination) AS dlat, ST_X(destination) AS dlon,
		       direction, seats_needed, luggage_count, tolerance_meters,
		       status, trip_id, scheduled_at, created_at, updated_at
		FROM ride_requests
		WHERE trip_id = $1
		ORDER BY created_at ASC
	`, tripID)
	if err != nil {
		return nil, nil, fmt.Errorf("get trip %d passengers: %w", tripID, err)
	}
	defer rows.Close()

	var passengers []model.RideRequest
	for rows.Next() {
		var rr model.RideRequest
		var tid *int64
		if err := rows.Scan(
			&rr.ID, &rr.UserID,
			&rr.Origin.Lat, &rr.Origin.Lon,
			&rr.Destination.Lat, &rr.Destination.Lon,
			&rr.Direction, &rr.SeatsNeeded, &rr.LuggageCount, &rr.ToleranceMeters,
			&rr.Status, &tid, &rr.ScheduledAt, &rr.CreatedAt, &rr.UpdatedAt,
		); err != nil {
			return nil, nil, fmt.Errorf("scan passenger: %w", err)
		}
		rr.TripID = tid
		passengers = append(passengers, rr)
	}

	return trip, passengers, rows.Err()
}
