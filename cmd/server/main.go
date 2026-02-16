package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/shiva/hintro/config"
	"github.com/shiva/hintro/internal/handler"
	"github.com/shiva/hintro/internal/middleware"
	"github.com/shiva/hintro/internal/repository"
	"github.com/shiva/hintro/internal/service"
	"github.com/shiva/hintro/pkg/cache"
	"github.com/shiva/hintro/pkg/db"
)

func main() {
	// ── Load configuration ──────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	ctx := context.Background()

	// ── Connect to PostgreSQL ───────────────────────────
	pgPool, err := db.NewPostgresPool(ctx, cfg.Postgres)
	if err != nil {
		log.Fatalf("failed to connect to PostgreSQL: %v", err)
	}
	defer pgPool.Close()
	log.Println("✓ PostgreSQL connected")

	// ── Connect to Redis ────────────────────────────────
	redisClient, err := cache.NewRedisClient(ctx, cfg.Redis)
	if err != nil {
		log.Fatalf("failed to connect to Redis: %v", err)
	}
	defer redisClient.Close()
	log.Println("✓ Redis connected")

	// ── Initialize layers ───────────────────────────────
	rideRepo := repository.NewRideRepository(pgPool)
	rideRequestRepo := repository.NewRideRequestRepository(pgPool)
	bookingRepo := repository.NewBookingRepository(pgPool)
	pricingRepo := repository.NewPricingRepository(pgPool, redisClient)

	matchingSvc := service.NewMatchingService(rideRepo)
	bookingSvc := service.NewBookingService(bookingRepo, matchingSvc)
	cancelSvc := service.NewCancelService(bookingRepo, pricingRepo)
	pricingSvc := service.NewPricingService(pricingRepo, service.DefaultFareConfig())

	matchHandler := handler.NewMatchHandler(matchingSvc)
	bookingHandler := handler.NewBookingHandler(bookingSvc)
	cancelHandler := handler.NewCancelHandler(cancelSvc)
	pricingHandler := handler.NewPricingHandler(pricingSvc)
	rideHandler := handler.NewRideHandler(rideRequestRepo)

	// ── Setup router ────────────────────────────────────
	router := mux.NewRouter()

	// Health check endpoint.
	router.HandleFunc("/health", healthHandler(pgPool, redisClient)).Methods(http.MethodGet)

	// API v1 routes.
	api := router.PathPrefix("/api/v1").Subrouter()
	// Ride request CRUD
	api.HandleFunc("/rides", rideHandler.CreateRide).Methods(http.MethodPost)
	api.HandleFunc("/rides/{id}", rideHandler.GetRide).Methods(http.MethodGet)
	// Matching, booking, cancellation
	api.HandleFunc("/match/{request_id}", matchHandler.MatchRideRequest).Methods(http.MethodPost)
	api.HandleFunc("/book/{request_id}", bookingHandler.BookRide).Methods(http.MethodPost)
	api.HandleFunc("/cancel/{request_id}", cancelHandler.CancelRide).Methods(http.MethodPost)
	api.HandleFunc("/fare/estimate", pricingHandler.EstimateFare).Methods(http.MethodPost)

	// Wrap with CORS so Swagger UI (and other browser clients) can call the API.
	handler := middleware.CORS(router)

	// ── Start HTTP server ───────────────────────────────
	srv := &http.Server{
		Addr:         cfg.Server.ServerAddr(),
		Handler:      handler,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	// Start in a goroutine so we can listen for shutdown signals.
	go func() {
		log.Printf("🚀 Server listening on %s", cfg.Server.ServerAddr())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	// ── Graceful shutdown ───────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("⏳ Shutting down server...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("server forced to shutdown: %v", err)
	}

	log.Println("✅ Server gracefully stopped")
}

// HealthResponse represents the /health endpoint response.
type HealthResponse struct {
	Status   string            `json:"status"`
	Services map[string]string `json:"services"`
}

// healthHandler returns an HTTP handler that checks PG and Redis connectivity.
func healthHandler(pgPool *pgxpool.Pool, redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := HealthResponse{
			Status:   "ok",
			Services: make(map[string]string),
		}

		if err := db.HealthCheck(r.Context(), pgPool); err != nil {
			resp.Status = "degraded"
			resp.Services["postgres"] = "unhealthy: " + err.Error()
		} else {
			resp.Services["postgres"] = "healthy"
		}

		if err := cache.HealthCheck(r.Context(), redisClient); err != nil {
			resp.Status = "degraded"
			resp.Services["redis"] = "unhealthy: " + err.Error()
		} else {
			resp.Services["redis"] = "healthy"
		}

		w.Header().Set("Content-Type", "application/json")
		if resp.Status != "ok" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(resp)
	}
}
