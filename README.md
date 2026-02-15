# 🛫 Smart Airport Ride Pooling Backend

A high-performance ride pooling backend that groups airport-bound passengers into shared cabs, minimizing travel deviation while respecting seat/luggage constraints.

**Built for:** `<300ms latency` · `100 RPS` · `10,000 concurrent users`

**Tech Stack:** Go 1.22 · PostgreSQL 16 + PostGIS 3.4 · Redis 7 · Docker

---

## 📦 Quick Start

### Prerequisites

- [Docker](https://docs.docker.com/get-docker/) & Docker Compose

> **That's it.** No Go, PostgreSQL, or Redis installation needed.

### Run (Single Command)

```bash
docker-compose up --build -d
```

This automatically:
1. Builds the Go binary in a multi-stage Docker image
2. Starts PostgreSQL (PostGIS), Redis, and the application
3. Waits for database health checks to pass
4. Applies the schema migration
5. Starts the server on **`http://localhost:8080`**

### Verify

```bash
curl http://localhost:8080/health
```
```json
{"status":"ok","services":{"postgres":"healthy","redis":"healthy"}}
```

### Stop

```bash
docker-compose down       # Keep data
docker-compose down -v    # Wipe all data + volumes
```

---

## 🗄️ Project Structure

```
Hintro/
├── cmd/server/main.go              # HTTP entry point, router, graceful shutdown
├── config/config.go                # Viper-based configuration loader
├── pkg/
│   ├── db/postgres.go              # PostgreSQL connection pool (pgxpool)
│   ├── cache/redis.go              # Redis connection pool (go-redis)
│   └── geo/geo.go                  # Haversine distance, route time estimation
├── internal/
│   ├── model/model.go              # Domain models, enums, DTOs
│   ├── repository/
│   │   ├── ride_repository.go      # PostGIS spatial queries (matching)
│   │   ├── booking_repository.go   # Transactional booking (FOR UPDATE)
│   │   └── pricing_repository.go   # Demand/supply from Redis + PostGIS
│   ├── service/
│   │   ├── matching.go             # Greedy heuristic ride matcher
│   │   ├── booking.go              # Booking with pessimistic locking
│   │   └── pricing.go              # Dynamic fare + surge pricing
│   └── handler/
│       ├── handler.go              # Match endpoint
│       ├── booking_handler.go      # Booking endpoint
│       └── pricing_handler.go      # Fare estimation endpoint
├── migrations/
│   ├── 001_create_schema.up.sql    # Full schema (PostGIS, indexes, triggers)
│   └── 001_create_schema.down.sql  # Rollback script
├── Dockerfile                      # Multi-stage build (builder → alpine)
├── docker-compose.yml              # One-command orchestration
└── entrypoint.sh                   # Auto-migration on startup
```

---

## 🔌 API Endpoints

### `GET /health`

Health check for all dependencies.

```bash
curl http://localhost:8080/health
```

**Response** `200 OK`:
```json
{
  "status": "ok",
  "services": {
    "postgres": "healthy",
    "redis": "healthy"
  }
}
```

---

### `POST /api/v1/match/{request_id}`

Find a compatible existing trip for a pending ride request.

```bash
curl -X POST http://localhost:8080/api/v1/match/2
```

**Response** `200 OK` — Match found:
```json
{
  "trip_id": 1,
  "cab_id": 1,
  "added_detour_minutes": 0
}
```

**Response** `404` — No match:
```json
{
  "error": "no_match",
  "message": "No compatible trip found. A new trip should be created."
}
```

| Status | Meaning |
|--------|---------|
| `200` | Match found |
| `400` | Invalid `request_id` |
| `404` | Request not found / no match |
| `409` | Request already matched |

---

### `POST /api/v1/book/{request_id}`

Book a ride — finds a match (or creates a new trip) and reserves the seat atomically.

```bash
curl -X POST http://localhost:8080/api/v1/book/2
```

**Response** `200 OK`:
```json
{
  "trip_id": 1,
  "cab_id": 1,
  "request_id": 2,
  "seats_booked": 1,
  "remaining_seats": 2
}
```

| Status | Meaning |
|--------|---------|
| `200` | Booking successful |
| `400` | Invalid `request_id` |
| `404` | Request not found / no cab nearby |
| `408` | Timeout (lock contention) |
| `409` | Request not in `pending` state |
| `422` | Cab full / cab unavailable |

---

### `POST /api/v1/fare/estimate`

Calculate the fare with dynamic surge pricing.

```bash
curl -X POST http://localhost:8080/api/v1/fare/estimate \
  -H "Content-Type: application/json" \
  -d '{
    "origin_lat": 28.7041,
    "origin_lon": 77.1025,
    "dest_lat": 28.5562,
    "dest_lon": 77.0889
  }'
```

**Response** `200 OK`:
```json
{
  "base_fare_cents": 5000,
  "distance_fare_cents": 19799,
  "time_fare_cents": 6600,
  "subtotal_cents": 31399,
  "surge_multiplier": 1.5,
  "total_fare_cents": 47099,
  "distance_km": 16.5,
  "estimated_minutes": 33,
  "demand": 6,
  "supply": 2,
  "demand_supply_ratio": 3.0
}
```

**Pricing Formula:**

```
Price = (BaseFare + Distance × PerKmRate + Time × PerMinRate) × SurgeMultiplier
```

**Surge Tiers:**

| Demand/Supply Ratio | Multiplier |
|---------------------|------------|
| R ≤ 1.5 | 1.0× (normal) |
| R > 1.5 | 1.2× (moderate) |
| R > 2.0 | 1.5× (high) |

---

## 🏗️ Design Decisions

### Why PostGIS?

The core challenge is **spatial** — "find nearby passengers going to the airport." PostGIS provides:

- **`GEOMETRY(Point, 4326)`** — stores GPS coordinates in the WGS-84 standard
- **`ST_DWithin()`** — finds points within a real-world distance (meters, not degrees)
- **GIST Indexes** — spatial tree indexes that turn O(N) full-table scans into **O(log N)** lookups

Without PostGIS, finding "all pending requests within 2km" would require scanning every row and computing distance in application code. With GIST indexes, PostgreSQL does this in **<1ms** even with millions of rows.

### Why Pessimistic Locking (`SELECT ... FOR UPDATE`)?

The critical scenario: **two users book the last seat at the exact same millisecond.**

We use PostgreSQL's `SELECT ... FOR UPDATE` inside a `ReadCommitted` transaction:

```
User A: BEGIN → SELECT cab FOR UPDATE → (row LOCKED)
User B: BEGIN → SELECT cab FOR UPDATE → ⏳ BLOCKS (waiting)
User A: seats OK → UPDATE → COMMIT → lock released
User B: (unblocked) → re-reads → NO SEATS → ROLLBACK → 422 error
```

**Why not Optimistic Locking?** Optimistic locking (version columns + retry loops) adds application complexity and can cause retry storms under high contention. Pessimistic locking is simpler, deterministic, and PostgreSQL handles the queuing natively.

**Timeout safety:** A 5-second context deadline prevents deadlock starvation — if a lock wait exceeds this, the transaction aborts with a `408 Timeout` error.

### Why Redis for Surge Pricing?

Demand/supply counts change rapidly. Querying PostGIS on every fare estimate would add ~5ms of latency. Redis provides:

- **<1ms lookups** for cached demand/supply counts
- **30-second TTL** — stale data is acceptable for surge (it's an estimate)
- **Graceful degradation** — if Redis is down, the service falls back to PostGIS directly

---

## ⚡ Complexity Analysis

### Matching Algorithm — Greedy Heuristic

```
Total per request: O(log N + C × S²)
```

| Component | Complexity | Explanation |
|-----------|-----------|-------------|
| **PostGIS fetch** | O(log N) | GIST index scan on `ride_requests(origin)` |
| **Candidate loop** | O(C) | C ≤ 20 candidates (capped by LIMIT) |
| **Insertion scoring** | O(S²) | S ≤ 6 stops per trip (try each insertion point) |
| **Haversine distance** | O(1) | Constant-time trigonometry |

**In practice:** With C=20 and S=6, the inner loop executes 720 Haversine calculations — microseconds in Go. The GIST index handles millions of records. **Total latency: <5ms per request**, well within the 300ms constraint.

### Why Not Optimal (TSP)?

The Travelling Salesman Problem is NP-hard. For airport pooling, the greedy heuristic works because:

1. **One endpoint is fixed** (the airport) — this isn't general VRP
2. **Trips have ≤ 4-6 passengers** — the solution space is tiny
3. **Detour tolerance** acts as a hard filter — bad candidates are pruned early
4. The difference between greedy and optimal for 4-6 stops is negligible

---

## 📊 Database Schema

### Tables

| Table | Purpose | PostGIS Columns |
|-------|---------|-----------------|
| `users` | Passengers and drivers | — |
| `cabs` | Vehicles with capacity | `current_location` (Point) |
| `ride_requests` | Pickup/dropoff requests | `origin`, `destination` (Point) |
| `trips` | Grouped rides | `route_path` (LineString) |

### Key Indexes

| Index | Type | Purpose |
|-------|------|---------|
| `idx_ride_requests_origin_gist` | GIST | Core spatial matching query |
| `idx_ride_requests_status_created` | B-tree | FIFO queue for pending requests |
| `idx_cabs_location_gist` | GIST | Find nearest available cab |
| `idx_cabs_status_created` | B-tree | Available cab lookup |

---

## 🧪 Testing

Seed test data:
```bash
# Load test data (Delhi locations around IGI Airport)
Get-Content migrations/test_seed.sql -Raw | docker exec -i hintro-postgres psql -U hintro -d hintro_db

# Test matching
curl -X POST http://localhost:8080/api/v1/match/2

# Test booking
curl -X POST http://localhost:8080/api/v1/book/2

# Test fare estimate (Connaught Place → IGI Airport)
curl -X POST http://localhost:8080/api/v1/fare/estimate \
  -H "Content-Type: application/json" \
  -d '{"origin_lat":28.7041,"origin_lon":77.1025,"dest_lat":28.5562,"dest_lon":77.0889}'
```

Concurrency race test seed: `migrations/test_concurrency_seed.sql`

---

## 📝 License

This project was built as a backend systems design assignment.
