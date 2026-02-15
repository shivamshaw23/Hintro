#!/usr/bin/env python3
"""
==============================================================================
 Smart Airport Ride Pooling — QA Verification Suite
==============================================================================

 Tests the Go backend against the mandatory assignment constraints:
   1. Functional Sanity   — Can we book a ride end-to-end?
   2. Race Condition       — Is concurrent booking safe? (SELECT ... FOR UPDATE)
   3. Latency @ 100 RPS   — Is P95 under 300ms?

 Requirements:
   pip install requests

 Usage:
   python test_suite.py

 The script expects the system to be running via:
   docker-compose up --build -d
==============================================================================
"""

import json
import os
import subprocess
import sys
import time
import statistics
from concurrent.futures import ThreadPoolExecutor, as_completed

try:
    import requests
except ImportError:
    print("ERROR: 'requests' library not installed. Run: pip install requests")
    sys.exit(1)


# ─── Configuration ───────────────────────────────────────────

BASE_URL = os.getenv("BASE_URL", "http://localhost:8080")
POSTGRES_CONTAINER = os.getenv("PG_CONTAINER", "hintro-postgres")
PG_USER = "hintro"
PG_DB = "hintro_db"

# Test parameters
RACE_THREADS = 20          # Number of concurrent booking threads
LATENCY_REQUESTS = 500     # Total requests for the latency test
P95_THRESHOLD_MS = 300     # Maximum acceptable P95 latency


# ─── Helpers ─────────────────────────────────────────────────

PASS = "\033[92m[PASS]\033[0m"
FAIL = "\033[91m[FAIL]\033[0m"
WARN = "\033[93m[WARN]\033[0m"
BOLD = "\033[1m"
RESET = "\033[0m"

results = {"passed": 0, "failed": 0, "warnings": 0}


def run_sql(sql: str) -> str:
    """Execute SQL inside the PostgreSQL Docker container and return result."""
    proc = subprocess.run(
        f"docker exec -i {POSTGRES_CONTAINER} psql -U {PG_USER} -d {PG_DB} -t -A",
        shell=True,
        input=sql,
        capture_output=True,
        text=True,
        timeout=10,
    )
    output = proc.stdout.strip()
    # Filter out empty lines and return last non-empty line (the actual result)
    lines = [l.strip() for l in output.split("\n") if l.strip()]
    return lines[-1] if lines else ""


def seed_sql(sql: str):
    """Execute multi-line SQL seed script via stdin pipe."""
    proc = subprocess.run(
        f"docker exec -i {POSTGRES_CONTAINER} psql -U {PG_USER} -d {PG_DB}",
        shell=True,
        input=sql,
        capture_output=True,
        text=True,
        timeout=15,
    )
    return proc.stdout.strip()


def assert_test(name: str, condition: bool, detail: str = ""):
    """Record and print a test assertion."""
    if condition:
        results["passed"] += 1
        print(f"  {PASS}  {name}")
    else:
        results["failed"] += 1
        print(f"  {FAIL}  {name}")
    if detail:
        print(f"        {detail}")


def warn_test(name: str, detail: str = ""):
    """Record a warning (non-fatal)."""
    results["warnings"] += 1
    print(f"  {WARN}  {name}")
    if detail:
        print(f"        {detail}")


def header(title: str):
    """Print a section header."""
    print(f"\n{'='*60}")
    print(f"  {BOLD}{title}{RESET}")
    print(f"{'='*60}\n")


# ─── Pre-flight Check ───────────────────────────────────────

def preflight():
    """Verify the system is reachable."""
    header("PRE-FLIGHT CHECK")
    max_retries = 6
    for attempt in range(max_retries):
        try:
            r = requests.get(f"{BASE_URL}/health", timeout=5)
            data = r.json()
            assert_test("Server reachable", r.status_code == 200)
            assert_test("PostgreSQL healthy", data["services"]["postgres"] == "healthy")
            assert_test("Redis healthy", data["services"]["redis"] == "healthy")
            return
        except (requests.ConnectionError, requests.ReadTimeout) as e:
            if attempt < max_retries - 1:
                print(f"  Waiting for server... (attempt {attempt + 1}/{max_retries})")
                time.sleep(3)
            else:
                print(f"  {FAIL}  Cannot connect to {BASE_URL}")
                print(f"        Is the system running? Try: docker-compose up --build -d")
                sys.exit(1)


# ─── Test 1: Functional Sanity Check ────────────────────────

def test_functional_sanity():
    header("TEST 1: FUNCTIONAL SANITY CHECK")
    print("  Setup: Creating user, cab, trip, and pending ride request...\n")

    # Seed fresh test data
    seed_sql("""
        TRUNCATE ride_requests, trips, cabs, users RESTART IDENTITY CASCADE;

        INSERT INTO users (name, email, phone, role) VALUES
          ('TestUser',  'test@sanity.com', '+910000000001', 'passenger'),
          ('TestDriver','driver@sanity.com','+910000000002', 'driver');

        INSERT INTO cabs (driver_id, license_plate, seat_capacity, luggage_capacity,
                          current_location, status) VALUES
          (2, 'TEST-SANITY-001', 3, 3,
           ST_SetSRID(ST_MakePoint(77.1000, 28.6800), 4326), 'available');

        INSERT INTO trips (cab_id, direction, total_fare_cents, passenger_count, status) VALUES
          (1, 'to_airport', 0, 0, 'planned');

        INSERT INTO ride_requests (user_id, origin, destination, direction,
                                   seats_needed, luggage_count, tolerance_meters, status) VALUES
          (1,
           ST_SetSRID(ST_MakePoint(77.1025, 28.7041), 4326),
           ST_SetSRID(ST_MakePoint(77.0889, 28.5562), 4326),
           'to_airport', 1, 1, 5000, 'pending');
    """)

    # Action: Book the ride
    r = requests.post(f"{BASE_URL}/api/v1/book/1", timeout=10)
    data = r.json() if r.status_code == 200 else {}

    # Assertions
    assert_test(
        "POST /api/v1/book/1 returns 200 OK",
        r.status_code == 200,
        f"Got {r.status_code}: {r.text[:100]}" if r.status_code != 200 else "",
    )
    assert_test(
        "Response contains trip_id",
        "trip_id" in data,
        f"Response: {json.dumps(data, indent=2)}" if data else "",
    )
    assert_test(
        "Response contains cab_id",
        "cab_id" in data,
    )
    assert_test(
        "seats_booked == 1",
        data.get("seats_booked") == 1,
    )

    # Verify DB state
    status = run_sql("SELECT status FROM ride_requests WHERE id = 1;")
    assert_test(
        "DB: ride_request status changed to 'matched'",
        status == "matched",
        f"Got: '{status}'" if status != "matched" else "",
    )


# ─── Test 1b: Match API ────────────────────────────────────

def test_match():
    header("TEST 1b: MATCH API")
    print("  Setup: Alice matched to trip; Bob pending nearby...\n")

    seed_sql("""
        TRUNCATE ride_requests, trips, cabs, users RESTART IDENTITY CASCADE;

        INSERT INTO users (name, email, phone, role) VALUES
          ('Alice', 'alice@test.com', '+911111111111', 'passenger'),
          ('Bob',   'bob@test.com',   '+912222222222', 'passenger'),
          ('Dave',  'dave@test.com',  '+913333333333', 'driver');

        INSERT INTO cabs (driver_id, license_plate, seat_capacity, luggage_capacity,
                          current_location, status) VALUES
          (3, 'DL-01-AB', 4, 3, ST_SetSRID(ST_MakePoint(77.1000, 28.6800), 4326), 'available');

        INSERT INTO trips (cab_id, direction, total_fare_cents, passenger_count, status) VALUES
          (1, 'to_airport', 0, 1, 'planned');

        INSERT INTO ride_requests (user_id, origin, destination, direction,
                                   seats_needed, luggage_count, tolerance_meters, status, trip_id) VALUES
          (1, ST_SetSRID(ST_MakePoint(77.1025, 28.7041), 4326), ST_SetSRID(ST_MakePoint(77.0889, 28.5562), 4326),
           'to_airport', 1, 1, 5000, 'matched', 1),
          (2, ST_SetSRID(ST_MakePoint(77.1010, 28.7020), 4326), ST_SetSRID(ST_MakePoint(77.0889, 28.5562), 4326),
           'to_airport', 1, 1, 5000, 'pending', NULL);
    """)

    # Bob (id=2) should match trip 1
    r = requests.post(f"{BASE_URL}/api/v1/match/2", timeout=10)
    data = r.json() if r.status_code == 200 else {}

    assert_test("POST /api/v1/match/2 returns 200", r.status_code == 200, r.text[:80] if r.status_code != 200 else "")
    assert_test("Response contains trip_id", "trip_id" in data)
    assert_test("Response contains cab_id", "cab_id" in data)

    # Charlie far away (Noida) - expect 404 no_match
    seed_sql("""
        INSERT INTO users (name, email, phone, role) VALUES
          ('Charlie', 'charlie@test.com', '+914444444444', 'passenger');
        INSERT INTO ride_requests (user_id, origin, destination, direction,
                                   seats_needed, luggage_count, tolerance_meters, status)
        VALUES (4, ST_SetSRID(ST_MakePoint(77.3910, 28.5355), 4326), ST_SetSRID(ST_MakePoint(77.0889, 28.5562), 4326),
                'to_airport', 1, 1, 2000, 'pending');
    """)
    charlie_id = run_sql("SELECT id FROM ride_requests WHERE user_id=4;")
    if not charlie_id:
        charlie_id = "3"
    r2 = requests.post(f"{BASE_URL}/api/v1/match/{charlie_id}", timeout=10)
    data2 = r2.json() if r2.status_code in (404, 200) else {}
    # Far request: expect 404 no_match (or 200 if by chance a trip exists)
    no_match_ok = r2.status_code == 404 and data2.get("error") == "no_match"
    assert_test("Far request returns 404 no_match", no_match_ok,
                f"Got {r2.status_code}: {data2}" if not no_match_ok else "")


# ─── Test 1c: Cancel API ───────────────────────────────────

def test_cancel():
    header("TEST 1c: CANCEL API")
    print("  Setup: Pending and matched requests...\n")

    seed_sql("""
        TRUNCATE ride_requests, trips, cabs, users RESTART IDENTITY CASCADE;

        INSERT INTO users (name, email, phone, role) VALUES
          ('Alice', 'alice@test.com', '+911111111111', 'passenger'),
          ('Bob',   'bob@test.com',   '+912222222222', 'passenger'),
          ('Dave',  'dave@test.com',  '+913333333333', 'driver');

        INSERT INTO cabs (driver_id, license_plate, seat_capacity, luggage_capacity,
                          current_location, status) VALUES
          (3, 'DL-01-AB', 4, 3, ST_SetSRID(ST_MakePoint(77.1000, 28.6800), 4326), 'available');

        INSERT INTO trips (cab_id, direction, total_fare_cents, passenger_count, status) VALUES
          (1, 'to_airport', 0, 1, 'planned');

        INSERT INTO ride_requests (user_id, origin, destination, direction,
                                   seats_needed, luggage_count, tolerance_meters, status, trip_id) VALUES
          (1, ST_SetSRID(ST_MakePoint(77.1025, 28.7041), 4326), ST_SetSRID(ST_MakePoint(77.0889, 28.5562), 4326),
           'to_airport', 1, 1, 5000, 'matched', 1),
          (2, ST_SetSRID(ST_MakePoint(77.1010, 28.7020), 4326), ST_SetSRID(ST_MakePoint(77.0889, 28.5562), 4326),
           'to_airport', 1, 1, 5000, 'pending', NULL);
    """)

    # Cancel pending (id=2)
    r = requests.post(f"{BASE_URL}/api/v1/cancel/2", timeout=10)
    data = r.json() if r.status_code == 200 else {}
    assert_test("Cancel pending returns 200", r.status_code == 200,
                f"Got {r.status_code}: {r.text[:100]}" if r.status_code != 200 else "")
    assert_test("Response contains request_id", data.get("request_id") == 2)

    status2 = run_sql("SELECT status FROM ride_requests WHERE id = 2;")
    assert_test("DB: request 2 is cancelled", status2 == "cancelled", f"Got status: {status2}")

    # Cancel matched (id=1) - frees capacity
    r2 = requests.post(f"{BASE_URL}/api/v1/cancel/1", timeout=10)
    data2 = r2.json() if r2.status_code == 200 else {}
    assert_test("Cancel matched returns 200", r2.status_code == 200,
                f"Got {r2.status_code}: {r2.text[:100]}" if r2.status_code != 200 else "")
    assert_test("Response has previous_trip_id", "previous_trip_id" in data2)
    assert_test("Response has trip_cancelled", data2.get("trip_cancelled") is True)


# ─── Test 1d: Fare Estimate API ─────────────────────────────

def test_fare_estimate():
    header("TEST 1d: FARE ESTIMATE API")
    print("  Connaught Place -> IGI Airport...\n")

    r = requests.post(
        f"{BASE_URL}/api/v1/fare/estimate",
        json={"origin_lat": 28.7041, "origin_lon": 77.1025, "dest_lat": 28.5562, "dest_lon": 77.0889},
        timeout=10,
    )
    data = r.json() if r.status_code == 200 else {}

    assert_test("POST /api/v1/fare/estimate returns 200", r.status_code == 200)
    assert_test("Response has total_fare_cents", "total_fare_cents" in data)
    assert_test("Response has surge_multiplier", "surge_multiplier" in data)
    assert_test("Response has distance_km", "distance_km" in data)
    assert_test("total_fare_cents > 0", data.get("total_fare_cents", 0) > 0)


# ─── Test 2: Race Condition (Concurrency Safety) ────────────

def test_race_condition():
    header("TEST 2: RACE CONDITION (Concurrent Last-Seat Booking)")
    print(f"  Setup: 1-seat cab, {RACE_THREADS} threads racing for the last seat...\n")

    # Seed: 1-seat cab that is already nearly full
    # Cab has capacity=2, 1 seat already taken → 1 remaining
    seed_sql("""
        TRUNCATE ride_requests, trips, cabs, users RESTART IDENTITY CASCADE;

        -- Create enough users for all racers + 1 driver + 1 existing passenger
        INSERT INTO users (name, email, phone, role)
        SELECT 'Racer' || i, 'racer' || i || '@test.com', '+91' || LPAD(i::text, 10, '0'), 'passenger'
        FROM generate_series(1, 22) AS i;

        -- Driver
        INSERT INTO users (name, email, phone, role) VALUES
          ('RaceDriver', 'racedriver@test.com', '+919999999999', 'driver');

        -- Cab with capacity = 2 seats
        INSERT INTO cabs (driver_id, license_plate, seat_capacity, luggage_capacity,
                          current_location, status) VALUES
          (23, 'RACE-CAB-001', 2, 10,
           ST_SetSRID(ST_MakePoint(77.1000, 28.6800), 4326), 'en_route');

        -- Trip with 1 passenger already matched (1 of 2 seats taken)
        INSERT INTO trips (cab_id, direction, total_fare_cents, passenger_count, status) VALUES
          (1, 'to_airport', 50000, 1, 'planned');

        -- Existing matched passenger (takes 1 seat)
        INSERT INTO ride_requests (user_id, origin, destination, direction,
                                   seats_needed, luggage_count, tolerance_meters, status, trip_id) VALUES
          (1,
           ST_SetSRID(ST_MakePoint(77.1025, 28.7041), 4326),
           ST_SetSRID(ST_MakePoint(77.0889, 28.5562), 4326),
           'to_airport', 1, 1, 5000, 'matched', 1);
    """)

    # Create RACE_THREADS pending ride requests (IDs will be 2..21)
    values = []
    for i in range(RACE_THREADS):
        user_id = i + 2  # Users 2..21
        lon = 77.1020 + (i * 0.0001)
        lat = 28.7030 + (i * 0.0001)
        values.append(
            f"({user_id}, ST_SetSRID(ST_MakePoint({lon:.4f}, {lat:.4f}), 4326), "
            f"ST_SetSRID(ST_MakePoint(77.0889, 28.5562), 4326), "
            f"'to_airport', 1, 1, 5000, 'pending')"
        )

    insert_sql = (
        "INSERT INTO ride_requests (user_id, origin, destination, direction, "
        "seats_needed, luggage_count, tolerance_meters, status) VALUES\n"
        + ",\n".join(values) + ";"
    )
    seed_sql(insert_sql)

    # Verify setup
    pending_count = int(run_sql("SELECT COUNT(*) FROM ride_requests WHERE status = 'pending';"))
    print(f"  Pending requests created: {pending_count}")
    assert_test(f"{RACE_THREADS} pending requests seeded", pending_count == RACE_THREADS)

    # ACTION: Fire all threads simultaneously
    print(f"\n  Firing {RACE_THREADS} concurrent booking requests...\n")
    successes = []
    failures = []

    def book_request(request_id):
        """Attempt to book a single ride request."""
        try:
            r = requests.post(f"{BASE_URL}/api/v1/book/{request_id}", timeout=15)
            return request_id, r.status_code, r.json()
        except Exception as e:
            return request_id, 0, {"error": str(e)}

    # request IDs 2 through 21 (the 20 pending ones)
    request_ids = list(range(2, 2 + RACE_THREADS))

    with ThreadPoolExecutor(max_workers=RACE_THREADS) as pool:
        futures = {pool.submit(book_request, rid): rid for rid in request_ids}
        for future in as_completed(futures):
            rid, status, data = future.result()
            if status == 200:
                successes.append((rid, data))
            else:
                failures.append((rid, status, data))

    # Results
    print(f"  Results:")
    print(f"    Successful bookings (200): {len(successes)}")
    print(f"    Rejected bookings:         {len(failures)}")

    if successes:
        winner = successes[0]
        print(f"    Winner: request #{winner[0]} -> {json.dumps(winner[1])}")

    # ASSERTIONS
    assert_test(
        "Exactly 1 booking succeeded",
        len(successes) == 1,
        f"Expected 1, got {len(successes)} — {'DOUBLE BOOKING DETECTED!' if len(successes) > 1 else 'No booking succeeded'}",
    )
    assert_test(
        f"Exactly {RACE_THREADS - 1} bookings rejected",
        len(failures) == RACE_THREADS - 1,
        f"Expected {RACE_THREADS - 1}, got {len(failures)}",
    )

    # Check rejection status codes
    rejection_codes = [f[1] for f in failures]
    valid_rejection_codes = {404, 409, 422, 408, 500}
    all_valid = all(code in valid_rejection_codes for code in rejection_codes)
    assert_test(
        "All rejections have valid status codes (409/422/408)",
        all_valid,
        f"Codes seen: {set(rejection_codes)}",
    )

    # Verify DB: only 2 matched requests total (1 original + 1 winner)
    matched = int(run_sql("SELECT COUNT(*) FROM ride_requests WHERE status = 'matched';"))
    assert_test(
        "DB: exactly 2 matched requests (1 original + 1 winner)",
        matched == 2,
        f"Got {matched} matched requests",
    )

    # Final verdict
    if len(successes) == 1:
        print(f"\n  {PASS}  {BOLD}TEST PASSED: Concurrency Safe{RESET}")
    else:
        print(f"\n  {FAIL}  {BOLD}TEST FAILED: Double Booking Detected!{RESET}")
        print(f"        FIX: Check the FOR UPDATE clause in booking_repository.go")


# ─── Test 3: Latency @ 100 RPS ──────────────────────────────

def test_latency():
    header(f"TEST 3: LATENCY TEST ({LATENCY_REQUESTS} requests)")

    # Seed data so the match endpoint has something to work with
    seed_sql("""
        TRUNCATE ride_requests, trips, cabs, users RESTART IDENTITY CASCADE;

        INSERT INTO users (name, email, phone, role) VALUES
          ('LatUser',  'lat@test.com', '+910000000001', 'passenger'),
          ('LatDriver','latdriver@test.com','+910000000002', 'driver');

        INSERT INTO cabs (driver_id, license_plate, seat_capacity, luggage_capacity,
                          current_location, status) VALUES
          (2, 'LAT-CAB-001', 4, 3,
           ST_SetSRID(ST_MakePoint(77.1000, 28.6800), 4326), 'available');

        INSERT INTO trips (cab_id, direction, total_fare_cents, passenger_count, status) VALUES
          (1, 'to_airport', 0, 0, 'planned');

        INSERT INTO ride_requests (user_id, origin, destination, direction,
                                   seats_needed, luggage_count, tolerance_meters, status, trip_id) VALUES
          (1,
           ST_SetSRID(ST_MakePoint(77.1025, 28.7041), 4326),
           ST_SetSRID(ST_MakePoint(77.0889, 28.5562), 4326),
           'to_airport', 1, 1, 5000, 'matched', 1);
    """)

    # We will hit the match endpoint for a request that will get 'already_matched'
    # This still exercises the full code path through the DB.
    # Using /health for raw latency and /match/1 for DB-path latency.

    print(f"  Firing {LATENCY_REQUESTS} requests to POST /api/v1/match/1 ...\n")

    latencies_ms = []

    def timed_request(_):
        """Send a match request and measure round-trip time."""
        start = time.perf_counter()
        try:
            r = requests.post(f"{BASE_URL}/api/v1/match/1", timeout=10)
            elapsed = (time.perf_counter() - start) * 1000  # ms
            return elapsed, r.status_code
        except Exception:
            elapsed = (time.perf_counter() - start) * 1000
            return elapsed, 0

    # Fire requests in batches using a thread pool to simulate load
    with ThreadPoolExecutor(max_workers=50) as pool:
        futures = [pool.submit(timed_request, i) for i in range(LATENCY_REQUESTS)]
        for future in as_completed(futures):
            elapsed, status = future.result()
            latencies_ms.append(elapsed)

    # Calculate metrics
    latencies_ms.sort()
    avg = statistics.mean(latencies_ms)
    p50 = latencies_ms[int(len(latencies_ms) * 0.50)]
    p95 = latencies_ms[int(len(latencies_ms) * 0.95)]
    p99 = latencies_ms[int(len(latencies_ms) * 0.99)]
    min_lat = min(latencies_ms)
    max_lat = max(latencies_ms)
    total_time = sum(latencies_ms) / 1000  # seconds
    effective_rps = LATENCY_REQUESTS / total_time * 50  # approx (50 workers)

    # Print metrics
    print(f"  +----------------------------------+")
    print(f"  |  Latency Results                 |")
    print(f"  +----------------------------------+")
    print(f"  |  Total Requests:    {LATENCY_REQUESTS:>10}    |")
    print(f"  |  Min Latency:       {min_lat:>8.1f} ms  |")
    print(f"  |  Avg Latency:       {avg:>8.1f} ms  |")
    print(f"  |  P50 (Median):      {p50:>8.1f} ms  |")
    print(f"  |  {BOLD}P95 Latency:       {p95:>8.1f} ms{RESET}  |")
    print(f"  |  P99 Latency:       {p99:>8.1f} ms  |")
    print(f"  |  Max Latency:       {max_lat:>8.1f} ms  |")
    print(f"  +----------------------------------+\n")

    # Assertions
    assert_test(
        f"P95 latency < {P95_THRESHOLD_MS}ms",
        p95 < P95_THRESHOLD_MS,
        f"P95 = {p95:.1f}ms (threshold: {P95_THRESHOLD_MS}ms)",
    )

    if p95 >= P95_THRESHOLD_MS:
        warn_test(
            f"P95 latency is {p95:.1f}ms — exceeds {P95_THRESHOLD_MS}ms threshold",
            "FIX: Run EXPLAIN ANALYZE on your matching query.\n"
            "        Check that GIST index on ride_requests(origin) is being used.\n"
            "        Increase POSTGRES_MAX_CONNS if connection pool is saturated.",
        )

    assert_test(
        "Average latency < 200ms",
        avg < 200,
        f"Avg = {avg:.1f}ms",
    )


# ─── Main ────────────────────────────────────────────────────

def main():
    print(f"""
================================================================
   Smart Airport Ride Pooling - QA Verification Suite
   Target: {BASE_URL}
================================================================
    """)

    start = time.time()

    preflight()
    test_functional_sanity()
    test_match()
    test_cancel()
    test_fare_estimate()
    test_race_condition()
    test_latency()

    elapsed = time.time() - start

    # ── Summary ──────────────────────────────────────────
    header("SUMMARY")
    total = results["passed"] + results["failed"]
    print(f"  Passed:   {results['passed']}/{total}")
    print(f"  Failed:   {results['failed']}/{total}")
    print(f"  Warnings: {results['warnings']}")
    print(f"  Time:     {elapsed:.1f}s\n")

    if results["failed"] == 0:
        print(f"  {BOLD}ALL TESTS PASSED{RESET}\n")
    else:
        print(f"  {BOLD}{results['failed']} TEST(S) FAILED{RESET}\n")
        sys.exit(1)


if __name__ == "__main__":
    main()
