package testpostgres

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

var shared struct {
	once sync.Once
	dsn  string
	err  error
}

var schemaSequence atomic.Uint64

func URL(t testing.TB) string {
	t.Helper()
	shared.once.Do(start)
	if shared.err != nil {
		t.Fatalf("start test Postgres: %v", shared.err)
	}
	schema := "factory_test_" + strconv.Itoa(os.Getpid()) + "_" + strconv.FormatUint(schemaSequence.Add(1), 10)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, shared.dsn)
	if err != nil {
		t.Fatalf("connect to test Postgres: %v", err)
	}
	if _, err := connection.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		connection.Close(ctx)
		t.Fatalf("create test Postgres schema: %v", err)
	}
	if err := connection.Close(ctx); err != nil {
		t.Fatalf("close test Postgres setup connection: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		connection, err := pgx.Connect(cleanupContext, shared.dsn)
		if err != nil {
			t.Errorf("connect to clean test Postgres schema: %v", err)
			return
		}
		defer connection.Close(cleanupContext)
		if _, err := connection.Exec(cleanupContext, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Errorf("drop test Postgres schema: %v", err)
		}
	})
	parsed, err := url.Parse(shared.dsn)
	if err != nil {
		t.Fatalf("parse test Postgres URL: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func start() {
	if dsn := os.Getenv("FACTORY_TEST_DATABASE_URL"); dsn != "" {
		shared.dsn = dsn
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	container, err := postgres.Run(
		ctx,
		"postgres:17-alpine",
		postgres.WithDatabase("factory"),
		postgres.WithUsername("factory"),
		postgres.WithPassword("factory"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		shared.err = err
		return
	}
	shared.dsn, shared.err = container.ConnectionString(ctx, "sslmode=disable")
	if shared.err != nil {
		shared.err = fmt.Errorf("read test Postgres URL: %w", shared.err)
	}
}
