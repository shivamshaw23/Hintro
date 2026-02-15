-- ============================================================
-- TEST SEED DATA — Delhi IGI Airport (28.5562, 77.0889)
-- ============================================================

TRUNCATE ride_requests, trips, cabs, users RESTART IDENTITY CASCADE;

-- Users
INSERT INTO users (name, email, phone, role) VALUES
  ('Alice Passenger',  'alice@test.com',   '+911111111111', 'passenger'),
  ('Bob Passenger',    'bob@test.com',     '+912222222222', 'passenger'),
  ('Charlie Passenger','charlie@test.com', '+913333333333', 'passenger'),
  ('Dave Driver',      'dave@test.com',    '+914444444444', 'driver'),
  ('Eve Driver',       'eve@test.com',     '+915555555555', 'driver');

-- Cabs (Dave=id 4, Eve=id 5 after RESTART IDENTITY)
INSERT INTO cabs (driver_id, license_plate, seat_capacity, luggage_capacity, current_location, status) VALUES
  (4, 'DL-01-AB-1234', 4, 3, ST_SetSRID(ST_MakePoint(77.1000, 28.6800), 4326), 'available'),
  (5, 'DL-02-CD-5678', 6, 5, ST_SetSRID(ST_MakePoint(77.0950, 28.6700), 4326), 'available');

-- Trip: Cab 1, to_airport, Alice already matched
INSERT INTO trips (cab_id, direction, total_fare_cents, passenger_count, status) VALUES
  (1, 'to_airport', 50000, 1, 'planned');

-- Alice: MATCHED to trip 1 (Connaught Place → Airport)
INSERT INTO ride_requests (user_id, origin, destination, direction, seats_needed, luggage_count, tolerance_meters, status, trip_id) VALUES
  (1, ST_SetSRID(ST_MakePoint(77.1025, 28.7041), 4326), ST_SetSRID(ST_MakePoint(77.0889, 28.5562), 4326),
   'to_airport', 1, 1, 2000, 'matched', 1);

-- Bob: PENDING, ~250m from Alice (should MATCH trip 1)
INSERT INTO ride_requests (user_id, origin, destination, direction, seats_needed, luggage_count, tolerance_meters, status) VALUES
  (2, ST_SetSRID(ST_MakePoint(77.1010, 28.7020), 4326), ST_SetSRID(ST_MakePoint(77.0889, 28.5562), 4326),
   'to_airport', 1, 1, 2000, 'pending');

-- Charlie: PENDING, ~30km away in Noida (should NOT match)
INSERT INTO ride_requests (user_id, origin, destination, direction, seats_needed, luggage_count, tolerance_meters, status) VALUES
  (3, ST_SetSRID(ST_MakePoint(77.3910, 28.5355), 4326), ST_SetSRID(ST_MakePoint(77.0889, 28.5562), 4326),
   'to_airport', 1, 1, 2000, 'pending');

-- Verify
SELECT 'users' AS tbl, count(*) AS cnt FROM users
UNION ALL SELECT 'cabs', count(*) FROM cabs
UNION ALL SELECT 'trips', count(*) FROM trips
UNION ALL SELECT 'ride_requests', count(*) FROM ride_requests;

SELECT id, user_id, status, trip_id,
       ST_AsText(origin) AS origin_wkt
FROM ride_requests ORDER BY id;
