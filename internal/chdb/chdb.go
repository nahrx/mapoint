// Package chdb wraps the ClickHouse connection pool used to serve map data.
package chdb

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"se2026-titik-maps/internal/config"
)

// New opens a pooled, native-protocol connection to ClickHouse and verifies
// it with a ping so startup fails fast if the database is unreachable.
func New(cfg *config.Config) (driver.Conn, error) {
	protocol := clickhouse.Native
	if cfg.CHProtocol == "http" {
		protocol = clickhouse.HTTP
	}

	conn, err := clickhouse.Open(&clickhouse.Options{
		Protocol: protocol,
		Addr:     []string{fmt.Sprintf("%s:%d", cfg.CHHost, cfg.CHPort)},
		Auth: clickhouse.Auth{
			Database: cfg.CHDatabase,
			Username: cfg.CHUsername,
			Password: cfg.CHPassword,
		},
		DialTimeout:          5 * time.Second,
		MaxOpenConns:         20,
		MaxIdleConns:         10,
		ConnMaxLifetime:      time.Hour,
		ConnOpenStrategy:     clickhouse.ConnOpenInOrder,
		BlockBufferSize:      10,
		MaxCompressionBuffer: 10240,
	})
	if err != nil {
		return nil, fmt.Errorf("chdb: open: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		return nil, fmt.Errorf(
			"chdb: ping %s:%d (protocol=%s): %w — if auth fails on the native port, try setting CH_PROTOCOL=http and PORT=8123 in .env",
			cfg.CHHost, cfg.CHPort, cfg.CHProtocol, err,
		)
	}

	return conn, nil
}
