package storage

import (
	"context"
	"os"
	"testing"

	"go.uber.org/zap"
)

func TestAnalyticsPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("VIDSAVE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("VIDSAVE_TEST_DATABASE_URL is not set; requires disposable PostgreSQL database")
	}
	ctx := context.Background()
	store, err := New(dsn, zap.NewNop())
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer store.Close()
	if err := store.TouchUser(ctx, 1234567); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchUser(ctx, 1234567); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchUser(ctx, 7654321); err != nil {
		t.Fatal(err)
	}
	events := []MediaRequest{
		{TelegramUserID: 1234567, Platform: "youtube", ChatKind: "private", Status: "success", CacheKind: "source", DurationMS: 123},
		{TelegramUserID: 7654321, Platform: "instagram", ChatKind: "group", Status: "auth_required", CacheKind: "none", DurationMS: 456},
		{TelegramUserID: 1234567, Platform: "tiktok", ChatKind: "private", Status: "success", CacheKind: "sha256", DurationMS: 789},
	}
	for _, event := range events {
		if err := store.RecordRequest(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := store.Analytics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Users != 2 || stats.DAU != 2 || stats.WAU != 2 || stats.MAU != 2 || stats.NewUsers24h != 2 {
		t.Errorf("incorrect user stats: %+v", stats)
	}
	if stats.Requests != 3 || stats.Success != 2 || stats.Failed != 1 ||
		stats.SourceCache != 1 || stats.HashCache != 1 ||
		stats.Requests24h != 3 || stats.Success24h != 2 {
		t.Errorf("incorrect request stats: %+v", stats)
	}
	if len(stats.Platforms) != 3 || len(stats.Errors) != 1 || stats.Errors[0].Status != "auth_required" {
		t.Errorf("incorrect breakdowns: %+v", stats)
	}
	reader, err := OpenAnalytics(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	report, err := reader.Analytics(ctx)
	if err != nil || report.Requests != stats.Requests {
		t.Fatalf("read-only query failed: %+v %v", report, err)
	}
}
