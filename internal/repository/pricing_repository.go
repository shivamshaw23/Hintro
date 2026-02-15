package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/shiva/hintro/internal/model"
)

// PricingRepository provides demand/supply data for surge pricing.
type PricingRepository struct {
	pool  *pgxpool.Pool
	redis *redis.Client
}

// NewPricingRepository creates a new pricing repository.
func NewPricingRepository(pool *pgxpool.Pool, redis *redis.Client) *PricingRepository {
	return &PricingRepository{pool: pool, redis: redis}
}

// DemandSupply holds the counts for a geographic area.
type DemandSupply struct {
	Demand int     `json:"demand"` // PENDING ride requests in the area.
	Supply int     `json:"supply"` // AVAILABLE cabs in the area.
	Ratio  float64 `json:"ratio"`  // Demand / Supply (0 if supply is 0).
}

// ─── Redis-backed fast path ─────────────────────────────────

const (
	redisDemandKeyPrefix = "surge:demand:"
	redisSupplyKeyPrefix = "surge:supply:"
	redisCacheTTL        = 30 * time.Second // Cache for 30s to avoid DB hammering.
)

// geohashKey returns a truncated geohash string for Redis bucketing.
// We use PostgreSQL's ST_GeoHash with precision 5 (~4.9km × 4.9km cells).
func geohashKey(loc model.Location) string {
	// Precision 5 gives ~4.9km cells — good for city-level surge zones.
	return fmt.Sprintf("%.2f:%.2f", loc.Lat, loc.Lon)
}

// GetDemandSupply returns the demand/supply ratio for the area around a location.
//
// Strategy:
//  1. Try Redis cache first (fast path, <1ms).
//  2. On cache miss, query PostGIS (slow path, ~5ms), then cache in Redis.
//
// The counts are scoped to a radius around the given location, not a strict
// geohash cell, for more accurate surge detection.
func (r *PricingRepository) GetDemandSupply(
	ctx context.Context,
	location model.Location,
	radiusMeters int,
) (*DemandSupply, error) {

	cacheKey := geohashKey(location)

	// ── Fast path: Redis cache ──────────────────────────
	demandKey := redisDemandKeyPrefix + cacheKey
	supplyKey := redisSupplyKeyPrefix + cacheKey

	demandVal, errD := r.redis.Get(ctx, demandKey).Int()
	supplyVal, errS := r.redis.Get(ctx, supplyKey).Int()

	if errD == nil && errS == nil {
		// Cache hit — compute ratio and return.
		ds := &DemandSupply{
			Demand: demandVal,
			Supply: supplyVal,
		}
		if ds.Supply > 0 {
			ds.Ratio = float64(ds.Demand) / float64(ds.Supply)
		} else if ds.Demand > 0 {
			ds.Ratio = float64(ds.Demand) // Infinite demand, treat as demand value.
		}
		return ds, nil
	}

	// ── Slow path: PostGIS query ────────────────────────
	ds, err := r.queryDemandSupplyFromDB(ctx, location, radiusMeters)
	if err != nil {
		return nil, err
	}

	// Cache the result in Redis (fire-and-forget, don't block on errors).
	_ = r.redis.Set(ctx, demandKey, ds.Demand, redisCacheTTL).Err()
	_ = r.redis.Set(ctx, supplyKey, ds.Supply, redisCacheTTL).Err()

	return ds, nil
}

// queryDemandSupplyFromDB queries PostGIS for demand/supply in a radius.
//
// Demand = count of PENDING ride_requests whose origin is within radius.
// Supply = count of AVAILABLE cabs whose current_location is within radius.
//
// Both queries use GIST indexes for O(log N) performance.
func (r *PricingRepository) queryDemandSupplyFromDB(
	ctx context.Context,
	location model.Location,
	radiusMeters int,
) (*DemandSupply, error) {

	// Single query with two subqueries for efficiency.
	query := `
		SELECT
			(SELECT COUNT(*)
			 FROM ride_requests
			 WHERE status = 'pending'
			   AND ST_DWithin(
			         origin::geography,
			         ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography,
			         $3
			       )
			)::int AS demand,
			(SELECT COUNT(*)
			 FROM cabs
			 WHERE status = 'available'
			   AND current_location IS NOT NULL
			   AND ST_DWithin(
			         current_location::geography,
			         ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography,
			         $3
			       )
			)::int AS supply
	`

	ds := &DemandSupply{}
	err := r.pool.QueryRow(ctx, query,
		location.Lon, location.Lat,
		radiusMeters,
	).Scan(&ds.Demand, &ds.Supply)
	if err != nil {
		return nil, fmt.Errorf("query demand/supply: %w", err)
	}

	if ds.Supply > 0 {
		ds.Ratio = float64(ds.Demand) / float64(ds.Supply)
	} else if ds.Demand > 0 {
		ds.Ratio = float64(ds.Demand)
	}

	return ds, nil
}

// InvalidateSurgeCache clears the cached demand/supply for an area.
// Call this after a booking or new request to ensure fresh data.
func (r *PricingRepository) InvalidateSurgeCache(ctx context.Context, location model.Location) {
	cacheKey := geohashKey(location)
	_ = r.redis.Del(ctx, redisDemandKeyPrefix+cacheKey).Err()
	_ = r.redis.Del(ctx, redisSupplyKeyPrefix+cacheKey).Err()
}
