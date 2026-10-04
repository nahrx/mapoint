// Command se2026-titik-maps serves a web map of every titik in the
// dtsen.se2026_titik2 ClickHouse table, streaming only what the current
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
	"se2026-titik-maps/internal/regsosek"
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
	regsvc := regsosek.NewService(conn)

	// Logged rather than fatal, on purpose: the rest of the app already
	// tolerates ClickHouse being briefly unavailable at startup, and making
	// this fatal would turn a blip into a restart loop. But a missing
	// dictionary breaks /api/list and /api/points completely, so it has to
	// be loud here instead of showing up later as a stream of 500s.
	dictCtx, dictCancel := context.WithTimeout(context.Background(), 15*time.Second)
	if err := svc.CheckDictionaries(dictCtx); err != nil {
		log.Error("clickhouse dictionary check FAILED — /api/list and /api/points will not work", "err", err)
	} else {
		log.Info("clickhouse dictionaries ok")
	}
	dictCancel()

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

	// config.Account -> api.Account: the api package deliberately doesn't
	// import config, so the two carry the same shape separately.
	accounts := make([]api.Account, len(cfg.AuthUsers))
	for i, u := range cfg.AuthUsers {
		accounts[i] = api.Account{Username: u.Username, Password: u.Password}
	}
	// Which batch the menus show. Done before the first request is served
	// so no one ever sees the whole table stacked up.
	dayCtx, dayCancel := context.WithTimeout(context.Background(), 30*time.Second)
	day, err := svc.RefreshLatestDay(dayCtx)
	dayCancel()
	if err != nil {
		// Not fatal: without a day the queries simply don't pin one, which
		// is exactly how the dashboard behaved before created_at existed.
		log.Error("latest data day unknown, showing every batch", "err", err)
	} else {
		logLatestDay(log, day)
	}

	log.Info("dashboard accounts loaded", "count", len(accounts))

	srv := api.NewServer(svc, regsvc, conn, bounds, kabkotaList, mapPool, accounts, cfg.DataUpdatedAt, cfg.ReportKabKotaPassword, log)
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

// logLatestDay reports which batch the menus are pinned to, and says so
// loudly when rows are left out of it: rows with no created_at belong to
// no day and are invisible to every menu, which would otherwise look like
// data quietly going missing.
func logLatestDay(log *slog.Logger, d points.LatestDayInfo) {
	if d.Day == "" {
		log.Warn("no usable created_at found, showing every batch", "rows", d.TotalRows)
		return
	}
	log.Info("data day selected", "day", d.Day, "rows", d.DayRows, "table_rows", d.TotalRows)
	if d.NullRows > 0 {
		log.Warn("rows with no created_at are not shown in any menu", "rows", d.NullRows, "day", d.Day)
	}
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
			// Before the bounds, so a new batch is picked up by the very
			// same cycle that recomputes the extent over it.
			prevDay := svc.LatestDay()
			if day, err := svc.RefreshLatestDay(ctx); err != nil {
				log.Warn("latest data day refresh failed, keeping previous value", "err", err)
			} else if day.Day != prevDay {
				// Only on a change: a line every ten minutes saying the same
				// date would bury the one tick that matters.
				logLatestDay(log, day)
			}

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
