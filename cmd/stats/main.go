// Command stats reads aggregate analytics using a read-only PostgreSQL role.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"xa4yy_vidsave/internal/storage"
)

func main() {
	dsn := os.Getenv("VIDSAVE_STATS_DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "VIDSAVE_STATS_DATABASE_URL is not set")
		os.Exit(2)
	}
	store, err := storage.OpenAnalytics(dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot open analytics database:", err)
		os.Exit(1)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snapshot, err := store.Analytics(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "analytics query failed:", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(snapshot); err != nil {
		fmt.Fprintln(os.Stderr, "cannot serialize analytics:", err)
		os.Exit(1)
	}
}
