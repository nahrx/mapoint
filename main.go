// Command se2026-titik-maps serves a web map of every titik in the
// dtsen.se2026_titik ClickHouse table, streaming only what the current
// viewport needs so millions of rows load seamlessly.
package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"se2026-titik-maps/internal/api"
	"se2026-titik-maps/internal/chdb"
	"se2026-titik-maps/internal/config"
	"se2026-titik-maps/internal/mapdb"
	"se2026-titik-maps/internal/points"
	"se2026-titik-maps/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}

	log.Info("connecting to clickhouse", "host", cfg.CHHost, "port", cfg.CHPort, "database", cfg.CHDatabase)
	conn, err := chdb.New(cfg)
	if err != nil {
		return err
	}
	defer conn.Close()

	svc := points.NewService(conn)

	log.Info("computing dataset bounds (one-time full scan)")
	boundsCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	bounds, err := svc.DatasetBounds(boundsCtx)
	cancel()
	if err != nil {
		return err
	}
	log.Info("dataset bounds ready",
		"total", bounds.Total,
		"lat", []float64{bounds.MinLat, bounds.MaxLat},
		"lon", []float64{bounds.MinLon, bounds.MaxLon},
	)

	kabkotaCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	kabkotaList, err := svc.KabKotaList(kabkotaCtx)
	cancel()
	if err != nil {
		return err
	}
	log.Info("kabupaten/kota list ready", "count", len(kabkotaList))

	// The SubSLS polygon layer is optional: the map works fine without
	// it. A missing MAP_HOST just skips it silently; a configured-but-
	// unreachable Postgres logs a warning and continues without polygons,
	// rather than taking down a server that otherwise has everything it
	// needs.
	var mapPool *pgxpool.Pool
	if cfg.MapEnabled() {
		log.Info("connecting to postgis", "host", cfg.MapHost, "port", cfg.MapPort, "database", cfg.MapDatabase)
		mapCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		pool, err := mapdb.New(mapCtx, cfg)
		cancel()
		if err != nil {
			log.Warn("postgis unavailable, SubSLS polygons disabled", "err", err)
		} else {
			mapPool = pool
			defer mapPool.Close()
		}
	}

	staticFS, err := fs.Sub(web.Static, "static")
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := api.NewServer(svc, conn, bounds, kabkotaList, mapPool, log)
	go refreshBoundsPeriodically(ctx, svc, srv, log)

	httpServer := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      srv.Routes(http.FS(staticFS)),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

// refreshBoundsPeriodically keeps the cached dataset extent/total and the
// kabupaten/kota filter list up to date as fieldwork adds more rows (and
// potentially new regions), without requiring a server restart. A failed
// refresh is logged and skipped — the previous good value stays in use,
// since a transient ClickHouse hiccup here shouldn't take the map down.
func refreshBoundsPeriodically(ctx context.Context, svc *points.Service, srv *api.Server, log *slog.Logger) {
	const interval = 10 * time.Minute
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if b, err := svc.DatasetBounds(ctx); err != nil {
				log.Warn("bounds refresh failed, keeping previous value", "err", err)
			} else {
				srv.SetBounds(b)
				log.Info("dataset bounds refreshed", "total", b.Total)
			}

			if list, err := svc.KabKotaList(ctx); err != nil {
				log.Warn("kabkota list refresh failed, keeping previous value", "err", err)
			} else {
				srv.SetKabKota(list)
				log.Info("kabupaten/kota list refreshed", "count", len(list))
			}
		}
	}
}
