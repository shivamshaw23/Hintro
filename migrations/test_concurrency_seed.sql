-- ============================================================
-- CONCURRENCY TEST SEED DATA
-- Cab 1 has 4 seats. 3 seats already taken. 1 seat remaining.
-- Two new pending requests will try to book the last seat.
-- ============================================================

TRUNCATE ride_requests, trips, cabs, users RESTART IDENTITY CASCADE;

-- Users
INSERT INTO users (name, email, phone, role) VALUES
  ('Alice',  'alice@test.com',   '+911111111111', 'passenger'),
  ('Bob',    'bob@test.com',     '+912222222222', 'passenger'),
  ('Charlie','charlie@test.com', '+913333333333', 'passenger'),
  ('Dave',   'dave@test.com',    '+914444444444', 'driver'),
  ('Racer1', 'racer1@test.com',  '+916666666666', 'passenger'),
  ('Racer2', 'racer2@test.com',  '+917777777777', 'passenger');

-- Cab: 4 seat capacity
INSERT INTO cabs (driver_id, license_plate, seat_capacity, luggage_capacity, current_location, status) VALUES
  (4, 'DL-01-AB-1234', 4, 4, ST_SetSRID(ST_MakePoint(77.1000, 28.6800), 4326), 'en_route');

-- Trip: already has 3 passengers
INSERT INTO trips (cab_id, direction, total_fare_cents, passenger_count, status) VALUES
  (1, 'to_airport', 50000, 3, 'planned');

-- 3 matched requests (filling 3 of 4 seats)
INSERT INTO ride_requests (user_id, origin, destination, direction, seats_needed, luggage_count, tolerance_meters, status, trip_id) VALUES
  (1, ST_SetSRID(ST_MakePoint(77.1025, 28.7041), 4326), ST_SetSRID(ST_MakePoint(77.0889, 28.5562), 4326), 'to_airport', 1, 1, 2000, 'matched', 1),
  (2, ST_SetSRID(ST_MakePoint(77.1015, 28.7030), 4326), ST_SetSRID(ST_MakePoint(77.0889, 28.5562), 4326), 'to_airport', 1, 1, 2000, 'matched', 1),
  (3, ST_SetSRID(ST_MakePoint(77.1005, 28.7010), 4326), ST_SetSRID(ST_MakePoint(77.0889, 28.5562), 4326), 'to_airport', 1, 1, 2000, 'matched', 1);

-- TWO RACERS: both PENDING, nearby, will try to book the LAST seat
-- Racer1 = request ID 4, Racer2 = request ID 5
INSERT INTO ride_requests (user_id, origin, destination, direction, seats_needed, luggage_count, tolerance_meters, status) VALUES
  (5, ST_SetSRID(ST_MakePoint(77.1020, 28.7035), 4326), ST_SetSRID(ST_MakePoint(77.0889, 28.5562), 4326), 'to_airport', 1, 1, 2000, 'pending'),
  (6, ST_SetSRID(ST_MakePoint(77.1022, 28.7037), 4326), ST_SetSRID(ST_MakePoint(77.0889, 28.5562), 4326), 'to_airport', 1, 1, 2000, 'pending');

-- Verify: should show 3 matched + 2 pending, cab has 4 seat capacity
SELECT 'Cab capacity: ' || seat_capacity AS info FROM cabs WHERE id=1;
SELECT 'Current load: ' || count(*) || ' matched passengers' AS info FROM ride_requests WHERE trip_id=1 AND status='matched';
SELECT id, user_id, status, trip_id FROM ride_requests ORDER BY id;
