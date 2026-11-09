//go:build !enterprise

package main

import (
	"context"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

const edition = "core"

// prepareEnterprise: Core has no Enterprise tables, audit, or extra default rules.
func prepareEnterprise(context.Context, *pgxpool.Pool, []byte) error { return nil }

// wireEnterprise: Core has no Enterprise features.
func wireEnterprise(context.Context, wiring) error {
	if os.Getenv("CROSS_ORG_ENABLED") == "true" {
		log.Printf("Core edition has no cross-org features, ignoring CROSS_ORG_ENABLED")
	}
	return nil
}
