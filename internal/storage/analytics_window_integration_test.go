package storage

import (
	"context"
	"os"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestAnalyticsWindowPostgres(t *testing.T) {
	dsn := os.Getenv("VIDSAVE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("disposable PostgreSQL fixture required")
	}
	store, err := New(dsn, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tx := store.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	s := &Storage{db: tx, log: zap.NewNop()}
	if err := tx.Exec("TRUNCATE media_requests, bot_users, media_cache RESTART IDENTITY").Error; err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	users := []BotUser{
		{TelegramUserID: 1, FirstSeenAt: now.Add(-34 * 24 * time.Hour), LastSeenAt: now.Add(-10 * time.Minute)},
		{TelegramUserID: 2, FirstSeenAt: now.Add(-5 * 24 * time.Hour), LastSeenAt: now.Add(-25 * time.Hour)},
		{TelegramUserID: 3, FirstSeenAt: now.Add(-time.Hour), LastSeenAt: now.Add(-time.Hour)},
	}
	for _, u := range users {
		if err := tx.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
	}
	requests := []MediaRequest{
		{TelegramUserID: 1, Platform: "youtube", ChatKind: "private", Status: "success", CacheKind: "source", CreatedAt: now.Add(-5 * time.Minute)},
		{TelegramUserID: 2, Platform: "instagram", ChatKind: "private", Status: "auth_required", CacheKind: "none", CreatedAt: now.Add(-2 * 24 * time.Hour)},
		{TelegramUserID: 3, Platform: "tiktok", ChatKind: "private", Status: "success", CacheKind: "sha256", CreatedAt: now.Add(-20 * 24 * time.Hour)},
		{TelegramUserID: 1, Platform: "youtube", ChatKind: "private", Status: "success", CacheKind: "none", CreatedAt: now.Add(-40 * 24 * time.Hour)},
	}
	for _, req := range requests {
		if err := tx.Create(&req).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, video := range []struct {
		key string
		ago time.Duration
	}{
		{"youtube:ancient", -90 * 24 * time.Hour},
		{"instagram:month", -20 * 24 * time.Hour},
		{"tiktok:week", -2 * 24 * time.Hour},
		{"youtube:today", -5 * time.Minute},
	} {
		if err := tx.Create(&MediaCache{
			SourceKey: video.key, SHA256: video.key, TgFileID: video.key,
			TgFileUniqueID: video.key, SizeBytes: 1200,
			CreatedAt: now.Add(video.ago), LastUsedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		period                                            AnalyticsPeriod
		users, newUsers, requests, success, errors, cache int64
		buckets                                           int
		videos                                            int64
		rate                                              float64
	}{
		{Period24h, 2, 1, 1, 1, 0, 1, 12, 1, 100},
		{Period7d, 3, 2, 2, 1, 1, 1, 7, 2, 50},
		{Period30d, 3, 2, 3, 2, 1, 2, 10, 3, 66.7},
		{PeriodAll, 3, 3, 4, 3, 1, 2, 12, 4, 75},
	}
	for _, tc := range cases {
		t.Run(string(tc.period), func(t *testing.T) {
			snap, err := s.analyticsWindowAt(context.Background(), tc.period, now)
			if err != nil {
				t.Fatal(err)
			}
			if snap.Users != tc.users || snap.NewUsers != tc.newUsers || snap.Requests != tc.requests || snap.Success != tc.success ||
				snap.Errors != tc.errors || snap.CacheHits != tc.cache || len(snap.Buckets) != tc.buckets {
				t.Fatalf("unexpected aggregation period=%s snap=%+v", tc.period, snap)
			}
			if snap.SuccessRate == nil || *snap.SuccessRate != tc.rate {
				t.Fatalf("incorrect success rate: %+v", snap.SuccessRate)
			}
			var sum int64
			for _, count := range snap.Buckets {
				sum += count
			}
			if sum != tc.requests {
				t.Fatalf("timeline sum %d != requests %d", sum, tc.requests)
			}
			if len(snap.Platforms) != 3 {
				t.Fatalf("missing platforms: %+v", snap.Platforms)
			}
			if snap.Videos != tc.videos || len(snap.VideoBuckets) != tc.buckets || len(snap.VideoPlatforms) != 3 {
				t.Fatalf("invalid historical video metrics period=%s %+v", tc.period, snap)
			}
			var videoSum int64
			for _, count := range snap.VideoBuckets {
				videoSum += count
			}
			if videoSum != tc.videos {
				t.Fatalf("video timeline sum %d != video count %d", videoSum, tc.videos)
			}
			var platformSum int64
			for _, p := range snap.VideoPlatforms {
				platformSum += p.Count
			}
			if platformSum != tc.videos {
				t.Fatalf("video platform sum %d != count %d", platformSum, tc.videos)
			}
			if tc.period == PeriodAll && snap.Historical != 4 {
				t.Fatalf("historic cache must be separate: %+v", snap)
			}
			if tc.period != PeriodAll && snap.Historical != 0 {
				t.Fatal("historical cache must show only for all")
			}
		})
	}
	if _, err := s.analyticsWindowAt(context.Background(), "invalid", now); err == nil {
		t.Fatal("unvalidated periods accepted")
	}
}

func TestAnalyticsWindowZeroRequests(t *testing.T) {
	dsn := os.Getenv("VIDSAVE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("disposable PostgreSQL fixture required")
	}
	store, err := New(dsn, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tx := store.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Exec("TRUNCATE media_requests, bot_users, media_cache RESTART IDENTITY").Error; err != nil {
		t.Fatal(err)
	}
	s := &Storage{db: tx, log: zap.NewNop()}
	snap, err := s.analyticsWindowAt(context.Background(), Period24h, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if snap.SuccessRate != nil || snap.Requests != 0 || snap.Users != 0 || snap.Historical != 0 || snap.Videos != 0 {
		t.Fatalf("no traffic should not display a misleading 0%%: %+v", snap)
	}
}
