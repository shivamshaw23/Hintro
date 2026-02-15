// Package repository provides database access for the ride pooling system.
//
// All spatial queries use PostGIS functions and the GIST indexes created
// in the schema migration (001_create_schema.up.sql).
package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shiva/hintro/internal/model"
)

// RideRepository provides database access for ride matching operations.
type RideRepository struct {
	pool *pgxpool.Pool
}

// NewRideRepository creates a new repository backed by the given PG pool.
func NewRideRepository(pool *pgxpool.Pool) *RideRepository {
	return &RideRepository{pool: pool}
}

// GetRideRequest fetches a single ride request by ID.
// Uses SELECT ... FOR UPDATE when forUpdate is true (row-level locking).
func (r *RideRepository) GetRideRequest(ctx context.Context, id int64, forUpdate bool) (*model.RideRequest, error) {
	lockClause := ""
	if forUpdate {
		lockClause = "FOR UPDATE"
	}

	query := fmt.Sprintf(`
		SELECT id, user_id,
		       ST_Y(origin) AS origin_lat, ST_X(origin) AS origin_lon,
		       ST_Y(destination) AS dest_lat, ST_X(destination) AS dest_lon,
		       direction, seats_needed, luggage_count, tolerance_meters,
		       status, trip_id, scheduled_at, created_at, updated_at
		FROM ride_requests
		WHERE id = $1
		%s`, lockClause)

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

// FindNearbyCandidateTrips finds active trips whose existing passengers have
// origins within `radiusMeters` of the given point, going in the same direction.
//
// This is the KEY spatial query that leverages the GIST index on ride_requests(origin).
//
// SQL strategy:
//  1. Use ST_DWithin on ride_requests.origin to find nearby matched requests.
//  2. JOIN through trips → cabs to get capacity info.
//  3. Aggregate current load (seats + luggage) per trip.
//  4. Filter to trips that are 'planned' (not yet departed).
//
// The query uses the geography cast (::geography) so radiusMeters is in real meters,
// not degrees — PostGIS handles the projection automatically.
//
// Complexity: O(log N) for the GIST index scan + O(K) for the K results.
func (r *RideRepository) FindNearbyCandidateTrips(
	ctx context.Context,
	origin model.Location,
	direction model.TripDirection,
	radiusMeters int,
) ([]model.CandidateTrip, error) {

	query := `
		SELECT
			t.id                AS trip_id,
			t.cab_id,
			t.direction,
			c.seat_capacity,
			c.luggage_capacity,
			COALESCE(SUM(rr.seats_needed), 0)::int   AS current_load,
			COALESCE(SUM(rr.luggage_count), 0)::int   AS current_luggage,
			ST_Distance(
				ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography,
				ST_Centroid(ST_Collect(rr.origin))::geography
			) AS distance_to_req
		FROM trips t
		JOIN cabs c ON c.id = t.cab_id
		JOIN ride_requests rr ON rr.trip_id = t.id AND rr.status = 'matched'
		WHERE t.status = 'planned'
		  AND t.direction = $3
		  AND ST_DWithin(
		        rr.origin::geography,
		        ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography,
		        $4
		      )
		GROUP BY t.id, t.cab_id, t.direction, c.seat_capacity, c.luggage_capacity
		ORDER BY distance_to_req ASC
		LIMIT 20
	`

	rows, err := r.pool.Query(ctx, query,
		origin.Lon, origin.Lat, // ST_MakePoint takes (lon, lat)
		direction,
		radiusMeters,
	)
	if err != nil {
		return nil, fmt.Errorf("find nearby candidates: %w", err)
	}
	defer rows.Close()

	var candidates []model.CandidateTrip
	for rows.Next() {
		var ct model.CandidateTrip
		if err := rows.Scan(
			&ct.TripID, &ct.CabID, &ct.Direction,
			&ct.SeatCapacity, &ct.LuggageCapacity,
			&ct.CurrentLoad, &ct.CurrentLuggage,
			&ct.DistanceToReq,
		); err != nil {
			return nil, fmt.Errorf("scan candidate trip: %w", err)
		}
		candidates = append(candidates, ct)
	}

	return candidates, rows.Err()
}

// FindPendingRequestsNearby returns PENDING ride requests whose origin
// is within `radiusMeters` of the given point, going in the same direction.
//
// This directly hits the GIST index `idx_ride_requests_origin_gist` and the
// composite index `idx_ride_requests_status_direction`.
//
// Used for initial clustering: "who else is nearby and wants to go the same way?"
//
// Complexity: O(log N) GIST scan + O(K) results.
func (r *RideRepository) FindPendingRequestsNearby(
	ctx context.Context,
	origin model.Location,
	direction model.TripDirection,
	radiusMeters int,
	excludeID int64,
	limit int,
) ([]model.RideRequest, error) {

	query := `
		SELECT id, user_id,
		       ST_Y(origin) AS origin_lat, ST_X(origin) AS origin_lon,
		       ST_Y(destination) AS dest_lat, ST_X(destination) AS dest_lon,
		       direction, seats_needed, luggage_count, tolerance_meters,
		       status, trip_id, scheduled_at, created_at, updated_at
		FROM ride_requests
		WHERE status = 'pending'
		  AND direction = $3
		  AND id != $5
		  AND ST_DWithin(
		        origin::geography,
		        ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography,
		        $4
		      )
		ORDER BY created_at ASC
		LIMIT $6
	`

	rows, err := r.pool.Query(ctx, query,
		origin.Lon, origin.Lat,
		direction,
		radiusMeters,
		excludeID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("find pending nearby: %w", err)
	}
	defer rows.Close()

	var results []model.RideRequest
	for rows.Next() {
		var rr model.RideRequest
		var tripID *int64
		if err := rows.Scan(
			&rr.ID, &rr.UserID,
			&rr.Origin.Lat, &rr.Origin.Lon,
			&rr.Destination.Lat, &rr.Destination.Lon,
			&rr.Direction, &rr.SeatsNeeded, &rr.LuggageCount, &rr.ToleranceMeters,
			&rr.Status, &tripID, &rr.ScheduledAt, &rr.CreatedAt, &rr.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan pending request: %w", err)
		}
		rr.TripID = tripID
		results = append(results, rr)
	}

	return results, rows.Err()
}

// UpdateRequestStatus sets the status and optional trip_id of a ride request.
// Uses row-level locking (the caller should be inside a transaction).
func (r *RideRepository) UpdateRequestStatus(
	ctx context.Context,
	requestID int64,
	status model.RequestStatus,
	tripID *int64,
) error {
	query := `
		UPDATE ride_requests
		SET status = $2, trip_id = $3
		WHERE id = $1
	`
	_, err := r.pool.Exec(ctx, query, requestID, status, tripID)
	if err != nil {
		return fmt.Errorf("update request %d status: %w", requestID, err)
	}
	return nil
}

// GetTripStops returns the origins of all matched passengers in a trip,
// ordered by creation time, plus the final destination (for route building).
func (r *RideRepository) GetTripStops(ctx context.Context, tripID int64) ([]model.Location, error) {
	query := `
		SELECT ST_Y(origin) AS lat, ST_X(origin) AS lon
		FROM ride_requests
		WHERE trip_id = $1 AND status = 'matched'
		ORDER BY created_at ASC
	`
	rows, err := r.pool.Query(ctx, query, tripID)
	if err != nil {
		return nil, fmt.Errorf("get trip %d stops: %w", tripID, err)
	}
	defer rows.Close()

	var stops []model.Location
	for rows.Next() {
		var loc model.Location
		if err := rows.Scan(&loc.Lat, &loc.Lon); err != nil {
			return nil, fmt.Errorf("scan stop: %w", err)
		}
		stops = append(stops, loc)
	}
	return stops, rows.Err()
}
