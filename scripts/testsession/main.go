// Command testsession mints a studio session token for a THROWAWAY database, so scripts that drive a
// freshly started server (restore-test, the verify skill) can call the authenticated API. It is a
// separate program that writes through the store code; there is no bypass flag in the server, so a
// normal server run has no way to skip authentication.
//
// Safety: it refuses unless the database host is loopback and the database name is one of the
// throwaway patterns below. The live database ("studio") can never match. The token is printed to
// stdout only; callers must keep it out of logs and command lines.
//
//	STUDIO_DATABASE_URL=postgres://.../verify_123 go run ./scripts/testsession
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/fiat/content-studio/internal/auth"
	"github.com/fiat/content-studio/internal/store"
)

var throwaway = regexp.MustCompile(`^(verify_|studio_restore_|studio_rt_src_)[a-z0-9_]+$`)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "testsession:", err)
		os.Exit(1)
	}
}

// validate refuses anything but a loopback host and a throwaway database name.
func validate(url string) error {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return fmt.Errorf("invalid STUDIO_DATABASE_URL")
	}
	if ip := net.ParseIP(cfg.Host); cfg.Host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("refusing: database host is not loopback")
	}
	if !throwaway.MatchString(cfg.Database) {
		return fmt.Errorf("refusing: database %q is not a throwaway (verify_*, studio_restore_*, studio_rt_src_*)", cfg.Database)
	}
	return nil
}

func run() error {
	url := os.Getenv("STUDIO_DATABASE_URL")
	if url == "" {
		return fmt.Errorf("STUDIO_DATABASE_URL is required")
	}
	if err := validate(url); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.Open(ctx, url)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := store.Migrate(ctx, db); err != nil {
		return err
	}
	svc, err := auth.New(db, auth.Config{RPID: "localhost", Origins: []string{"http://localhost"}, MaxTTL: 2 * time.Hour})
	if err != nil {
		return err
	}
	token, err := svc.CreateSession(ctx, nil, "test")
	if err != nil {
		return err
	}
	fmt.Println(token)
	return nil
}
