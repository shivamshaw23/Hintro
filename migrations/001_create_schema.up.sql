-- ============================================================
-- Smart Airport Ride Pooling — Database Schema
-- Migration: 001_create_schema (UP)
-- Requires: PostGIS extension
-- ============================================================

BEGIN;

-- ── Enable PostGIS ──────────────────────────────────────────
CREATE EXTENSION IF NOT EXISTS postgis;

-- ── ENUM Types ──────────────────────────────────────────────
CREATE TYPE user_role        AS ENUM ('passenger', 'driver', 'admin');
CREATE TYPE cab_status       AS ENUM ('available', 'en_route', 'on_trip', 'offline');
CREATE TYPE request_status   AS ENUM ('pending', 'matched', 'confirmed', 'cancelled', 'completed');
CREATE TYPE trip_status      AS ENUM ('planned', 'in_progress', 'completed', 'cancelled');
CREATE TYPE trip_direction   AS ENUM ('to_airport', 'from_airport');

-- ── 1. Users ────────────────────────────────────────────────
CREATE TABLE users (
    id              BIGSERIAL       PRIMARY KEY,
    name            VARCHAR(255)    NOT NULL,
    email           VARCHAR(255)    NOT NULL UNIQUE,
    phone           VARCHAR(20)     NOT NULL UNIQUE,
    role            user_role       NOT NULL DEFAULT 'passenger',
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

-- Index for user lookups by role.
CREATE INDEX idx_users_role ON users (role);

-- ── 2. Cabs ─────────────────────────────────────────────────
-- Tracks each cab's capacity, real-time location, and availability.
-- current_location uses PostGIS GEOMETRY(Point, 4326) (EPSG:4326 = WGS 84).
CREATE TABLE cabs (
    id                  BIGSERIAL           PRIMARY KEY,
    driver_id           BIGINT              NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    license_plate       VARCHAR(20)         NOT NULL UNIQUE,
    seat_capacity       SMALLINT            NOT NULL DEFAULT 4
                                            CHECK (seat_capacity BETWEEN 1 AND 8),
    luggage_capacity    SMALLINT            NOT NULL DEFAULT 3
                                            CHECK (luggage_capacity BETWEEN 0 AND 10),
    current_location    GEOMETRY(Point, 4326),
    status              cab_status          NOT NULL DEFAULT 'offline',
    created_at          TIMESTAMPTZ         NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ         NOT NULL DEFAULT NOW()
);

-- Spatial index for nearest-cab queries.
CREATE INDEX idx_cabs_location_gist ON cabs USING GIST (current_location);

-- Fast lookup of available cabs.
CREATE INDEX idx_cabs_status ON cabs (status);

-- Composite: find available cabs ordered by registration time.
CREATE INDEX idx_cabs_status_created ON cabs (status, created_at);

-- ── 3. Ride Requests ────────────────────────────────────────
-- Each row = one passenger's request to travel to/from the airport.
-- origin & destination are PostGIS points.
-- tolerance_meters = max detour a passenger accepts for pooling.
CREATE TABLE ride_requests (
    id                  BIGSERIAL           PRIMARY KEY,
    user_id             BIGINT              NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    origin              GEOMETRY(Point, 4326) NOT NULL,
    destination         GEOMETRY(Point, 4326) NOT NULL,
    direction           trip_direction      NOT NULL,
    seats_needed        SMALLINT            NOT NULL DEFAULT 1
                                            CHECK (seats_needed BETWEEN 1 AND 6),
    luggage_count       SMALLINT            NOT NULL DEFAULT 0
                                            CHECK (luggage_count BETWEEN 0 AND 8),
    tolerance_meters    INT                 NOT NULL DEFAULT 2000
                                            CHECK (tolerance_meters BETWEEN 0 AND 10000),
    status              request_status      NOT NULL DEFAULT 'pending',
    trip_id             BIGINT,             -- Set when matched to a trip (FK added after trips table).
    scheduled_at        TIMESTAMPTZ,        -- Optional: future ride time.
    created_at          TIMESTAMPTZ         NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ         NOT NULL DEFAULT NOW()
);

-- *** CRITICAL INDEX: GIST on origin for spatial proximity queries ***
-- This powers the core matching query — "find all pending requests near this origin".
CREATE INDEX idx_ride_requests_origin_gist ON ride_requests USING GIST (origin);

-- GIST on destination for grouping passengers headed to the same airport terminal.
CREATE INDEX idx_ride_requests_destination_gist ON ride_requests USING GIST (destination);

-- *** CRITICAL INDEX: Composite on (status, created_at) for FIFO matching ***
-- Enables fast lookup: "get the oldest pending requests" without full table scan.
CREATE INDEX idx_ride_requests_status_created ON ride_requests (status, created_at);

-- Composite: filter pending requests by direction and time.
CREATE INDEX idx_ride_requests_status_direction ON ride_requests (status, direction, created_at);

-- User lookup: "show me my active requests".
CREATE INDEX idx_ride_requests_user_status ON ride_requests (user_id, status);

-- ── 4. Trips ────────────────────────────────────────────────
-- A trip groups multiple ride_requests into one shared cab journey.
-- route_path stores the optimized multi-stop path as a PostGIS LINESTRING.
CREATE TABLE trips (
    id                  BIGSERIAL           PRIMARY KEY,
    cab_id              BIGINT              NOT NULL REFERENCES cabs(id) ON DELETE CASCADE,
    direction           trip_direction      NOT NULL,
    route_path          GEOMETRY(LineString, 4326),
    total_distance_m    INT,                -- Calculated route distance in meters.
    total_fare_cents    INT                 NOT NULL DEFAULT 0
                                            CHECK (total_fare_cents >= 0),
    passenger_count     SMALLINT            NOT NULL DEFAULT 0,
    status              trip_status         NOT NULL DEFAULT 'planned',
    started_at          TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ         NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ         NOT NULL DEFAULT NOW()
);

-- Composite: "find active trips" for the dispatcher.
CREATE INDEX idx_trips_status_created ON trips (status, created_at);

-- Cab lookup: "what trips are assigned to this cab?"
CREATE INDEX idx_trips_cab_status ON trips (cab_id, status);

-- Spatial index on route for geo-queries like "trips passing through area X".
CREATE INDEX idx_trips_route_gist ON trips USING GIST (route_path);

-- ── Foreign Key: ride_requests.trip_id → trips.id ───────────
-- Added after trips table exists to avoid circular dependency.
ALTER TABLE ride_requests
    ADD CONSTRAINT fk_ride_requests_trip
    FOREIGN KEY (trip_id) REFERENCES trips(id) ON DELETE SET NULL;

CREATE INDEX idx_ride_requests_trip ON ride_requests (trip_id);

-- ── Updated-at trigger function ─────────────────────────────
-- Automatically sets updated_at on every UPDATE, for all tables.
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_users_updated_at
    BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_cabs_updated_at
    BEFORE UPDATE ON cabs FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_ride_requests_updated_at
    BEFORE UPDATE ON ride_requests FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_trips_updated_at
    BEFORE UPDATE ON trips FOR EACH ROW EXECUTE FUNCTION set_updated_at();

COMMIT;
