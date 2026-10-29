// Package storetest gives each test its own freshly migrated Postgres database.
package storetest

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/store"
)

const defaultAdminURL = "postgres://studio:studio@127.0.0.1:5433/postgres"

func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	admin := os.Getenv("STUDIO_TEST_DATABASE_URL")
	if admin == "" {
		admin = defaultAdminURL
	}
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect to test postgres (start it with `make db`): %v", err)
	}
	name := fmt.Sprintf("studio_test_%d", rand.Int64())
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	pool, err := store.Open(ctx, strings.Replace(admin, "/postgres", "/"+name, 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := conn.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test db: %v", err)
		}
		conn.Close(ctx)
	})
	if err := store.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

// Episode inserts a project and an episode and returns their IDs.
func Episode(t *testing.T, db *pgxpool.Pool) (projectID, episodeID int64) {
	t.Helper()
	ctx := context.Background()
	if err := db.QueryRow(ctx, `INSERT INTO projects (name, profile) VALUES ('test', 'fictional_series') RETURNING id`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO episodes (project_id, number, title, idea, target_duration_s)
		VALUES ($1, 1, 'ep', 'idea', 60) RETURNING id`, projectID).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}
	return projectID, episodeID
}

// AllowArtifactTampering disables the artifact immutability trigger for this test only, so a test can
// simulate a damaged or swapped artifact. Production code never does this.
func AllowArtifactTampering(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `ALTER TABLE artifacts DISABLE TRIGGER artifacts_immutable_row`); err != nil {
		t.Fatal(err)
	}
}
