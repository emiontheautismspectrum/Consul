// Package db opens PostgreSQL connections (through PgBouncer) and runs migrations.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// Open connects through PgBouncer. Transaction pooling breaks server-side prepared
// statements, so pgx is switched to the simple protocol; queries stay parameterized ($1, $2…).
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", withSimpleProtocol(dsn))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := ping(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Migrate applies embedded migrations over a direct (non-pooled) connection. A PostgreSQL
// advisory session lock serializes concurrent runs (several API instances starting at once,
// parallel test packages): one applies the migrations, the others wait and find nothing to do.
func Migrate(ctx context.Context, dsn string, migrations fs.FS) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := ping(ctx, db); err != nil {
		return err
	}
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(2, 60)) // up to 2 min
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations, goose.WithSessionLocker(locker))
	if err != nil {
		return err
	}
	results, err := p.Up(ctx)
	for _, r := range results {
		slog.Info("migration applied", "version", r.Source.Version, "duration_ms", r.Duration.Milliseconds())
	}
	return err
}

func ping(ctx context.Context, db *sql.DB) error {
	var err error
	for i := 0; i < 15; i++ {
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = db.PingContext(c)
		cancel()
		if err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("db ping: %w", err)
}

func withSimpleProtocol(dsn string) string {
	if strings.Contains(dsn, "default_query_exec_mode") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "default_query_exec_mode=simple_protocol"
}
