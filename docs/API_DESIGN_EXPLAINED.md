# API Design Questions Answered

## 1. "Why can I call Match API multiple times with the same ID?"

**Answer: This is CORRECT behavior.**

The **Match API (`POST /api/v1/match/{request_id}`) is READ-ONLY**. It:
- Checks if a compatible trip exists for the request
- Returns match details (trip_id, cab_id, detour) if found
- **Does NOT change the request status** — it stays `pending` until you call **Book**

**Why this design?**
- Allows users to check for matches before committing to book
- You can call match multiple times to see if better matches appear (e.g., as more trips are created)
- The actual booking (status change) happens in the **Book API**

**Flow:**
```
1. Create Request → status = "pending"
2. Match API → returns match (status still "pending")
3. Match API again → still returns match (status still "pending")
4. Book API → status changes to "matched" + seat reserved
5. Match API after booking → returns 409 "already_matched"
```

---

## 2. "Where do users pass their address? How do they get lat/lon?"

**Current Design:**
- Users must provide **latitude/longitude directly** in the request body
- No geocoding (address → coordinates) is built into this backend

**Example Request:**
```json
POST /api/v1/rides
{
  "user_id": 1,
  "origin_lat": 28.7041,
  "origin_lon": 77.1025,
  "dest_lat": 28.5562,
  "dest_lon": 77.0889,
  "direction": "to_airport",
  "seats_needed": 1,
  "luggage_count": 1,
  "tolerance_meters": 2000
}
```

**How users get lat/lon in production:**
1. **Mobile app / Frontend** uses:
   - **GPS** (device location) for origin
   - **Google Maps Geocoding API** or **Mapbox Geocoding** to convert addresses to coordinates
   - User types "123 Main St, Delhi" → frontend calls geocoding API → gets lat/lon → sends to backend

2. **This backend** focuses on **matching/booking logic**, not geocoding. In a full system:
   - Frontend handles geocoding
   - Backend receives lat/lon (as it does now)

**Why this separation?**
- Geocoding APIs have rate limits and costs
- Frontend can cache geocoding results
- Backend stays focused on core business logic

---

## 3. "Why seeding data? Why can't we create new data?"

**Previous Issue (NOW FIXED):**
- The `CreateRide` endpoint existed in code but **wasn't wired** in `main.go`
- So you couldn't create requests via API → had to use seed data

**Now Fixed:**
- `POST /api/v1/rides` is now wired and working
- You can create new ride requests via API
- Seed data is still useful for **testing** (quick setup with known data)

**When to use each:**
- **Seed data**: Quick testing, demos, integration tests
- **Create API**: Real user flows, production

---

## 4. "Is all the logic correct?"

**Yes, with one clarification:**

### Match API Logic ✅
- Checks if request is `pending` (line 79-81 in `matching.go`)
- If already matched → returns 409 "already_matched"
- If pending → searches for compatible trips
- **Doesn't update status** (correct — Book API does that)

### Book API Logic ✅
- Calls Match internally
- If match found → uses that trip
- If no match → creates new trip
- **Updates status to "matched"** + reserves seat (pessimistic locking)

### Cancel API Logic ✅
- PENDING → just updates status
- MATCHED → decrements trip count, frees cab if last passenger

### Potential Edge Cases:
1. **Race condition**: Two users match the same trip → Book API handles this with `SELECT ... FOR UPDATE` locking ✅
2. **Match then Book**: Match finds trip, but by the time Book runs, trip is full → Book API re-checks capacity ✅

---

## 5. "How does Match API really work?"

**Algorithm (Greedy Heuristic):**

1. **Fetch** request (must be `pending`)
2. **Spatial Query**: PostGIS `ST_DWithin` finds trips within `tolerance_meters` (default 2km) of request origin
3. **Filter**: Check seat capacity + luggage capacity
4. **Score**: For each candidate trip:
   - Simulate inserting the new pickup into the route
   - Calculate added detour time
   - Check if detour ≤ tolerance
5. **Select**: Return trip with **lowest detour** that fits constraints

**Why it allows multiple calls:**
- Match is **idempotent** (same input → same output)
- Status only changes on **Book**
- This allows users to "check again" if they want

---

## Summary: What's Correct vs What Was Missing

| Aspect | Status | Notes |
|--------|--------|-------|
| **Match API (read-only)** | ✅ Correct | Intentionally doesn't update status |
| **Book API (updates status)** | ✅ Correct | Uses pessimistic locking |
| **Cancel API** | ✅ Correct | Handles PENDING and MATCHED |
| **Create Ride API** | ✅ **NOW FIXED** | Wasn't wired, now is |
| **Geocoding** | ⚠️ Not included | Frontend should handle this |
| **Address input** | ⚠️ Not included | Users provide lat/lon directly |

---

## Updated API Endpoints

After fixes, you now have **7 endpoints**:

1. `GET /health` — Health check
2. `POST /api/v1/rides` — **Create ride request** (NEW - now wired)
3. `GET /api/v1/rides/{id}` — Get request status (NEW - now wired)
4. `POST /api/v1/match/{request_id}` — Find match (read-only)
5. `POST /api/v1/book/{request_id}` — Book ride (updates status)
6. `POST /api/v1/cancel/{request_id}` — Cancel request
7. `POST /api/v1/fare/estimate` — Fare estimate
