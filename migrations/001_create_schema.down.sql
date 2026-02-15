-- ============================================================
-- Smart Airport Ride Pooling — Database Schema
-- Migration: 001_create_schema (DOWN / Rollback)
-- ============================================================

BEGIN;

-- Drop triggers first.
DROP TRIGGER IF EXISTS trg_trips_updated_at        ON trips;
DROP TRIGGER IF EXISTS trg_ride_requests_updated_at ON ride_requests;
DROP TRIGGER IF EXISTS trg_cabs_updated_at         ON cabs;
DROP TRIGGER IF EXISTS trg_users_updated_at        ON users;

-- Drop trigger function.
DROP FUNCTION IF EXISTS set_updated_at();

-- Drop tables in reverse dependency order.
DROP TABLE IF EXISTS trips          CASCADE;
DROP TABLE IF EXISTS ride_requests  CASCADE;
DROP TABLE IF EXISTS cabs           CASCADE;
DROP TABLE IF EXISTS users          CASCADE;

-- Drop ENUM types.
DROP TYPE IF EXISTS trip_direction;
DROP TYPE IF EXISTS trip_status;
DROP TYPE IF EXISTS request_status;
DROP TYPE IF EXISTS cab_status;
DROP TYPE IF EXISTS user_role;

-- NOTE: We intentionally do NOT drop the PostGIS extension,
-- as other schemas may depend on it.

COMMIT;
