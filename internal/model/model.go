// Package model contains domain models for the ride pooling system.
// These structs map to the PostgreSQL schema defined in migrations/001_create_schema.up.sql.
package model

import "time"

// ─── Enums ──────────────────────────────────────────────────

type UserRole string

const (
	RolePassenger UserRole = "passenger"
	RoleDriver    UserRole = "driver"
	RoleAdmin     UserRole = "admin"
)

type CabStatus string

const (
	CabAvailable CabStatus = "available"
	CabEnRoute   CabStatus = "en_route"
	CabOnTrip    CabStatus = "on_trip"
	CabOffline   CabStatus = "offline"
)

type RequestStatus string

const (
	RequestPending   RequestStatus = "pending"
	RequestMatched   RequestStatus = "matched"
	RequestConfirmed RequestStatus = "confirmed"
	RequestCancelled RequestStatus = "cancelled"
	RequestCompleted RequestStatus = "completed"
)

type TripStatus string

const (
	TripPlanned    TripStatus = "planned"
	TripInProgress TripStatus = "in_progress"
	TripCompleted  TripStatus = "completed"
	TripCancelled  TripStatus = "cancelled"
)

type TripDirection string

const (
	DirectionToAirport   TripDirection = "to_airport"
	DirectionFromAirport TripDirection = "from_airport"
)

// ─── Location ───────────────────────────────────────────────

// Location represents a WGS-84 geographic point (EPSG:4326).
type Location struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// ─── Domain Models ──────────────────────────────────────────

// User maps to the `users` table.
type User struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Phone     string    `json:"phone"`
	Role      UserRole  `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Cab maps to the `cabs` table.
type Cab struct {
	ID              int64     `json:"id"`
	DriverID        int64     `json:"driver_id"`
	LicensePlate    string    `json:"license_plate"`
	SeatCapacity    int       `json:"seat_capacity"`
	LuggageCapacity int       `json:"luggage_capacity"`
	CurrentLocation *Location `json:"current_location,omitempty"`
	Status          CabStatus `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// RideRequest maps to the `ride_requests` table.
type RideRequest struct {
	ID              int64         `json:"id"`
	UserID          int64         `json:"user_id"`
	Origin          Location      `json:"origin"`
	Destination     Location      `json:"destination"`
	Direction       TripDirection `json:"direction"`
	SeatsNeeded     int           `json:"seats_needed"`
	LuggageCount    int           `json:"luggage_count"`
	ToleranceMeters int           `json:"tolerance_meters"`
	Status          RequestStatus `json:"status"`
	TripID          *int64        `json:"trip_id,omitempty"`
	ScheduledAt     *time.Time    `json:"scheduled_at,omitempty"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
}

// Trip maps to the `trips` table.
type Trip struct {
	ID             int64         `json:"id"`
	CabID          int64         `json:"cab_id"`
	Direction      TripDirection `json:"direction"`
	RoutePath      []Location    `json:"route_path,omitempty"`
	TotalDistanceM *int          `json:"total_distance_m,omitempty"`
	TotalFareCents int           `json:"total_fare_cents"`
	PassengerCount int           `json:"passenger_count"`
	Status         TripStatus    `json:"status"`
	StartedAt      *time.Time    `json:"started_at,omitempty"`
	CompletedAt    *time.Time    `json:"completed_at,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

// ─── Matching–specific DTOs ─────────────────────────────────

// CandidateTrip is a denormalized view used by the matching engine.
// It combines Trip + Cab capacity + current load from a single DB query.
type CandidateTrip struct {
	TripID          int64      `json:"trip_id"`
	CabID           int64      `json:"cab_id"`
	Direction       TripDirection
	SeatCapacity    int
	LuggageCapacity int
	CurrentLoad     int        // Sum of seats_needed across matched passengers.
	CurrentLuggage  int        // Sum of luggage_count across matched passengers.
	Route           []Location // Ordered stops.
	DistanceToReq   float64    // Distance from the trip centroid to the new request (meters).
}

// MatchResult is returned by the matching service.
type MatchResult struct {
	TripID     int64   `json:"trip_id"`
	CabID      int64   `json:"cab_id"`
	AddedDetour float64 `json:"added_detour_minutes"`
}
