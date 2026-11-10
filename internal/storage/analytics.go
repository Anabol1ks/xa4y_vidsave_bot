package storage

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type BotUser struct {
	TelegramUserID int64     `gorm:"primaryKey"`
	FirstSeenAt    time.Time `gorm:"not null;index"`
	LastSeenAt     time.Time `gorm:"not null;index"`
}

type MediaRequest struct {
	ID             uint64    `gorm:"primaryKey"`
	TelegramUserID int64     `gorm:"not null;index"`
	Platform       string    `gorm:"size:20;not null;index"`
	ChatKind       string    `gorm:"size:20;not null"`
	Status         string    `gorm:"size:40;not null;index"`
	CacheKind      string    `gorm:"size:20;not null"`
	DurationMS     int64     `gorm:"not null"`
	CreatedAt      time.Time `gorm:"not null;index"`
}

// TouchUser counts only people who actually interact with the bot.
// Telegram user IDs (not usernames or message contents) are persisted.
func (s *Storage) TouchUser(ctx context.Context, userID int64) error {
	if userID <= 0 {
		return nil
	}
	now := time.Now().UTC()
	row := BotUser{TelegramUserID: userID, FirstSeenAt: now, LastSeenAt: now}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "telegram_user_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"last_seen_at": gorm.Expr("GREATEST(bot_users.last_seen_at, EXCLUDED.last_seen_at)"),
		}),
	}).Create(&row).Error
}

func (s *Storage) RecordRequest(ctx context.Context, event MediaRequest) error {
	if event.TelegramUserID <= 0 {
		return fmt.Errorf("missing Telegram user ID")
	}
	event.CreatedAt = time.Now().UTC()
	if event.DurationMS < 0 {
		event.DurationMS = 0
	}
	return s.db.WithContext(ctx).Create(&event).Error
}

// OpenAnalytics opens a read-only query connection WITHOUT AutoMigrate.
// Use a PostgreSQL role that has SELECT only, not the application's credentials.
func OpenAnalytics(dsn string) (*Storage, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(2)
	sqlDB.SetMaxIdleConns(1)
	return &Storage{db: db, log: zap.NewNop()}, nil
}

type PlatformStat struct {
	Platform string `json:"platform"`
	Requests int64  `json:"requests"`
	Success  int64  `json:"success"`
	Failed   int64  `json:"failed"`
}

type ErrorStat struct {
	Status string `json:"status"`
	Count  int64  `json:"count"`
}

type AnalyticsSnapshot struct {
	GeneratedAt    time.Time      `json:"generated_at"`
	Users          int64          `json:"users_since_tracking"`
	NewUsers24h    int64          `json:"new_users_24h"`
	DAU            int64          `json:"active_users_24h"`
	WAU            int64          `json:"active_users_7d"`
	MAU            int64          `json:"active_users_30d"`
	Requests       int64          `json:"requests_since_tracking"`
	Success        int64          `json:"successful_sends"`
	Failed         int64          `json:"failed_requests"`
	SourceCache    int64          `json:"successful_source_cache_hits"`
	HashCache      int64          `json:"successful_hash_dedup_hits"`
	Requests24h    int64          `json:"requests_24h"`
	Success24h     int64          `json:"success_24h"`
	Requests7d     int64          `json:"requests_7d"`
	Success7d      int64          `json:"success_7d"`
	Requests30d    int64          `json:"requests_30d"`
	Success30d     int64          `json:"success_30d"`
	CachedLinks    int64          `json:"historical_cached_links"`
	LegacyCacheHit int64          `json:"historical_cache_hit_counter"`
	Platforms      []PlatformStat `json:"platforms"`
	Errors         []ErrorStat    `json:"errors"`
}

func (s *Storage) Analytics(ctx context.Context) (AnalyticsSnapshot, error) {
	now := time.Now().UTC()
	d := s.db.WithContext(ctx)
	a := AnalyticsSnapshot{GeneratedAt: now, Platforms: []PlatformStat{}, Errors: []ErrorStat{}}
	type userCounts struct {
		Users       int64
		NewUsers24h int64
		DAU         int64
		WAU         int64
		MAU         int64
	}
	var u userCounts
	err := d.Raw(`
SELECT count(*) AS users,
  count(*) FILTER (WHERE first_seen_at >= ?) AS new_users24h,
  count(*) FILTER (WHERE last_seen_at >= ?) AS dau,
  count(*) FILTER (WHERE last_seen_at >= ?) AS wau,
  count(*) FILTER (WHERE last_seen_at >= ?) AS mau
FROM bot_users`, now.Add(-24*time.Hour), now.Add(-24*time.Hour), now.Add(-7*24*time.Hour), now.Add(-30*24*time.Hour)).Scan(&u).Error
	if err != nil {
		return a, fmt.Errorf("count users: %w", err)
	}
	a.Users, a.NewUsers24h, a.DAU, a.WAU, a.MAU = u.Users, u.NewUsers24h, u.DAU, u.WAU, u.MAU
	type requestCounts struct {
		Requests    int64
		Success     int64
		Failed      int64
		SourceCache int64
		HashCache   int64
		Requests24h int64
		Success24h  int64
		Requests7d  int64
		Success7d   int64
		Requests30d int64
		Success30d  int64
	}
	var r requestCounts
	err = d.Raw(`
SELECT count(*) AS requests,
  count(*) FILTER (WHERE status='success') AS success,
  count(*) FILTER (WHERE status<>'success') AS failed,
  count(*) FILTER (WHERE status='success' AND cache_kind='source') AS source_cache,
  count(*) FILTER (WHERE status='success' AND cache_kind='sha256') AS hash_cache,
  count(*) FILTER (WHERE created_at>=?) AS requests24h,
  count(*) FILTER (WHERE created_at>=? AND status='success') AS success24h,
  count(*) FILTER (WHERE created_at>=?) AS requests7d,
  count(*) FILTER (WHERE created_at>=? AND status='success') AS success7d,
  count(*) FILTER (WHERE created_at>=?) AS requests30d,
  count(*) FILTER (WHERE created_at>=? AND status='success') AS success30d
FROM media_requests`,
		now.Add(-24*time.Hour), now.Add(-24*time.Hour),
		now.Add(-7*24*time.Hour), now.Add(-7*24*time.Hour),
		now.Add(-30*24*time.Hour), now.Add(-30*24*time.Hour)).Scan(&r).Error
	if err != nil {
		return a, fmt.Errorf("count requests: %w", err)
	}
	a.Requests, a.Success, a.Failed, a.SourceCache, a.HashCache = r.Requests, r.Success, r.Failed, r.SourceCache, r.HashCache
	a.Requests24h, a.Success24h, a.Requests7d, a.Success7d = r.Requests24h, r.Success24h, r.Requests7d, r.Success7d
	a.Requests30d, a.Success30d = r.Requests30d, r.Success30d

	err = d.Raw(`
SELECT platform, count(*) AS requests,
 count(*) FILTER (WHERE status='success') AS success,
 count(*) FILTER (WHERE status<>'success') AS failed
FROM media_requests GROUP BY platform ORDER BY requests DESC`).Scan(&a.Platforms).Error
	if err != nil {
		return a, fmt.Errorf("platform stats: %w", err)
	}
	err = d.Raw(`
SELECT status, count(*) AS count FROM media_requests
WHERE status<>'success'
GROUP BY status ORDER BY count DESC LIMIT 6`).Scan(&a.Errors).Error
	if err != nil {
		return a, fmt.Errorf("error stats: %w", err)
	}
	// Scan aggregates into a flat struct: GORM must not attempt to treat
	// Platforms / Errors slices in AnalyticsSnapshot as ORM relations.
	var legacy struct {
		CachedLinks    int64
		LegacyCacheHit int64
	}
	err = d.Raw(`
SELECT count(*) AS cached_links, COALESCE(sum(hit_count),0) AS legacy_cache_hit
FROM media_cache WHERE deleted_at IS NULL`).Scan(&legacy).Error
	if err != nil {
		return a, fmt.Errorf("legacy cache stats: %w", err)
	}
	a.CachedLinks, a.LegacyCacheHit = legacy.CachedLinks, legacy.LegacyCacheHit
	return a, nil
}
