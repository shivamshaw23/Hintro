# Testing the APIs in Postman

Follow these steps so **all 5 endpoints** return success in Postman.

---

## 1. Start the backend and load test data

**Terminal (PowerShell):**

```powershell
cd c:\Users\shiva\OneDrive\Desktop\Hintro

# Start services (if not already running)
docker-compose up -d

# Load test data (required for match / book / cancel to work)
Get-Content migrations\test_seed.sql -Raw | docker exec -i hintro-postgres psql -U hintro -d hintro_db
```

Without the seed, **match**, **book**, and **cancel** will return **404** (no request or no match).

---

## 2. Import the collection

- In Postman: **File → Import** → select `docs/Hintro.postman_collection.json`.
- Collection **Variables**: `baseUrl` = `http://localhost:8080` (change only if your server is elsewhere).

---

## 3. Call the requests in this order

| # | Request | Expected |
|---|---------|----------|
| 1 | **Health** → Health Check | **200** — `"status":"ok"` |
| 2 | **Pricing** → Estimate Fare | **200** — fare breakdown (body is pre-filled) |
| 3 | **Matching** → Match Ride Request | **200** — `trip_id`, `cab_id` (request 2 is near an existing trip) |
| 4 | **Booking** → Book Ride | **200** — booking details (or **409** if 2 was already booked) |
| 5 | **Booking** → Cancel Ride | **200** — `request_id`, optional `previous_trip_id` |

- **Match** and **Book** use **request_id = 2** in the URL. After the seed, request 2 is **pending** and can be matched then booked.
- **Estimate Fare** is the only request with a **body**. Keep **Body → raw → JSON** and **Content-Type: application/json** (the collection sets this).

---

## 4. If something fails

| Symptom | Fix |
|--------|-----|
| **404** on match/book/cancel | Run the seed (step 1). Ensure you’re using request id **2** (or 3 for a “no match” test). |
| **404** on match for id 3 | Expected — request 3 is far away; no compatible trip. |
| **409** on book | Request is already matched/booked. Re-run the seed and try again, or use another request id. |
| **Failed to send / connection error** | Backend not running. Run `docker-compose up -d` and check `curl http://localhost:8080/health`. |
| **400** on Estimate Fare | In Postman, set **Body → raw**, type **JSON**, and send the exact keys: `origin_lat`, `origin_lon`, `dest_lat`, `dest_lon`. |

---

## 5. Quick re-test (fresh data)

To reset and test again:

```powershell
Get-Content migrations\test_seed.sql -Raw | docker exec -i hintro-postgres psql -U hintro -d hintro_db
```

Then in Postman: Health → Estimate Fare → Match (2) → Book (2) → Cancel (2).
