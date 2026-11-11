package storage

import (
	"context"
	"fmt"
	"math"
	"time"
)

type AnalyticsPeriod string

const (
	Period24h AnalyticsPeriod = "24h"
	Period7d  AnalyticsPeriod = "7d"
	Period30d AnalyticsPeriod = "30d"
	PeriodAll AnalyticsPeriod = "all"
)

func ParseAnalyticsPeriod(value string) (AnalyticsPeriod, bool) {
	p := AnalyticsPeriod(value)
	switch p {
	case Period24h, Period7d, Period30d, PeriodAll:
		return p, true
	default:
		return "", false
	}
}

// WidgetAnalytics is an aggregate-only DTO, with no user IDs or individual events.
type WidgetAnalytics struct {
	Version        int                   `json:"v"`
	Period         AnalyticsPeriod       `json:"p"`
	Revision       int64                 `json:"rev"`
	Users          int64                 `json:"u"`
	NewUsers       int64                 `json:"n"`
	Requests       int64                 `json:"r"`
	Success        int64                 `json:"s"`
	Errors         int64                 `json:"e"`
	CacheHits      int64                 `json:"c"`
	SuccessRate    *float64              `json:"rate"`
	Historical     int64                 `json:"hist,omitempty"`
	Videos         int64                 `json:"vid"` // unique source links in the historical cache, including pre-analytics
	VideoPlatforms []WindowVideoPlatform `json:"vp"`
	VideoBuckets   []int64               `json:"vb"`
	Platforms      []WindowPlatform      `json:"pf"`
	ErrorCodes     []WindowError         `json:"er"`
	Buckets        []int64               `json:"b"`
}

type WindowPlatform struct {
	Name     string `json:"n"`
	Requests int64  `json:"r"`
	Success  int64  `json:"s"`
}

type WindowError struct {
	Code  string `json:"n"`
	Count int64  `json:"c"`
}

// The cache predates analytics: one row represents one unique source link.
// These counts are NOT successful requests, downloads or distinct files by SHA.
type WindowVideoPlatform struct {
	Name  string `json:"n"`
	Count int64  `json:"c"`
}

func (s *Storage) AnalyticsWindow(ctx context.Context, period AnalyticsPeriod) (WidgetAnalytics, error) {
	return s.analyticsWindowAt(ctx, period, time.Now().UTC())
}

func (s *Storage) analyticsWindowAt(ctx context.Context, period AnalyticsPeriod, now time.Time) (WidgetAnalytics, error) {
	if _, ok := ParseAnalyticsPeriod(string(period)); !ok {
		return WidgetAnalytics{}, fmt.Errorf("unknown analytics period")
	}
	now = now.UTC()
	out := WidgetAnalytics{
		Version: 1, Period: period, Revision: now.UnixMicro(),
		Platforms:      []WindowPlatform{{Name: "instagram"}, {Name: "tiktok"}, {Name: "youtube"}},
		ErrorCodes:     []WindowError{},
		VideoPlatforms: []WindowVideoPlatform{{Name: "instagram"}, {Name: "tiktok"}, {Name: "youtube"}},
	}
	start := now
	switch period {
	case Period24h:
		start = now.Add(-24 * time.Hour)
	case Period7d:
		start = now.Add(-7 * 24 * time.Hour)
	case Period30d:
		start = now.Add(-30 * 24 * time.Hour)
	case PeriodAll:
		// All-time widgets deliberately start with the analytics rollout,
		// not the historic (user-less) media_cache table.
		var earliest struct{ Start *time.Time }
		if err := s.db.WithContext(ctx).Raw("SELECT MIN(created_at) AS start FROM media_requests").Scan(&earliest).Error; err != nil {
			return out, fmt.Errorf("analytics start: %w", err)
		}
		if earliest.Start != nil && earliest.Start.Before(now) {
			start = *earliest.Start
		} else {
			start = now.Add(-time.Hour)
		}
	}

	db := s.db.WithContext(ctx)
	var users struct {
		Active int64
		New    int64
	}
	if period == PeriodAll {
		if err := db.Raw("SELECT COUNT(*) AS active, COUNT(*) AS new FROM bot_users").Scan(&users).Error; err != nil {
			return out, fmt.Errorf("count all users: %w", err)
		}
	} else {
		if err := db.Raw(`SELECT
  COUNT(*) FILTER(WHERE last_seen_at >= ? AND last_seen_at <= ?) AS active,
  COUNT(*) FILTER(WHERE first_seen_at >= ? AND first_seen_at <= ?) AS new
FROM bot_users`, start, now, start, now).Scan(&users).Error; err != nil {
			return out, fmt.Errorf("count active users: %w", err)
		}
	}
	out.Users, out.NewUsers = users.Active, users.New

	where, args := "", []interface{}{}
	if period != PeriodAll {
		where, args = " WHERE created_at >= ? AND created_at <= ?", []interface{}{start, now}
	}
	var counts struct {
		Requests int64
		Success  int64
		Errors   int64
		Cache    int64
	}
	query := `SELECT COUNT(*) AS requests,
 COUNT(*) FILTER(WHERE status = 'success') AS success,
 COUNT(*) FILTER(WHERE status <> 'success') AS errors,
 COUNT(*) FILTER(WHERE status = 'success' AND cache_kind IN ('source','sha256')) AS cache
FROM media_requests` + where
	if err := db.Raw(query, args...).Scan(&counts).Error; err != nil {
		return out, fmt.Errorf("count analytics requests: %w", err)
	}
	out.Requests, out.Success, out.Errors, out.CacheHits = counts.Requests, counts.Success, counts.Errors, counts.Cache
	if counts.Requests != 0 {
		rate := math.Round(1000*float64(counts.Success)/float64(counts.Requests)) / 10
		out.SuccessRate = &rate
	}

	var platforms []struct {
		Platform string
		Requests int64
		Success  int64
	}
	if err := db.Raw(`SELECT platform, COUNT(*) AS requests,
 COUNT(*) FILTER(WHERE status = 'success') AS success
FROM media_requests`+where+" GROUP BY platform", args...).Scan(&platforms).Error; err != nil {
		return out, fmt.Errorf("analytics platforms: %w", err)
	}
	for _, item := range platforms {
		for i := range out.Platforms {
			if out.Platforms[i].Name == item.Platform {
				out.Platforms[i].Requests = item.Requests
				out.Platforms[i].Success = item.Success
			}
		}
	}

	errWhere := " WHERE status <> 'success'"
	if period != PeriodAll {
		errWhere += " AND created_at >= ? AND created_at <= ?"
	}
	if err := db.Raw(`SELECT status AS code, COUNT(*) AS count
FROM media_requests`+errWhere+" GROUP BY status ORDER BY count DESC, status LIMIT 3", args...).Scan(&out.ErrorCodes).Error; err != nil {
		return out, fmt.Errorf("analytics errors: %w", err)
	}
	if out.ErrorCodes == nil {
		out.ErrorCodes = []WindowError{}
	}

	n := 12
	switch period {
	case Period7d:
		n = 7
	case Period30d:
		n = 10
	}
	out.Buckets = make([]int64, n)
	span := now.Sub(start).Seconds() / float64(n)
	if span < 1 {
		span = 1
	}
	var bucketCounts []struct {
		Idx   int
		Count int64
	}
	if err := db.Raw(`
SELECT LEAST(?, GREATEST(0, FLOOR(EXTRACT(EPOCH FROM (created_at - ?)) / ?)::int)) AS idx,
 COUNT(*) AS count
FROM media_requests
WHERE created_at >= ? AND created_at <= ?
GROUP BY idx ORDER BY idx`, n-1, start, span, start, now).Scan(&bucketCounts).Error; err != nil {
		return out, fmt.Errorf("analytics timeline: %w", err)
	}
	for _, point := range bucketCounts {
		if point.Idx >= 0 && point.Idx < n {
			out.Buckets[point.Idx] = point.Count
		}
	}
	// Historical video links existed long before bot_users and media_requests.
	// Compute them independently, over the FULL lifetime when period=all.
	videoStart := start
	if period == PeriodAll {
		var earliest struct{ Start *time.Time }
		if err := db.Raw("SELECT MIN(created_at) AS start FROM media_cache WHERE deleted_at IS NULL AND created_at <= ?", now).Scan(&earliest).Error; err != nil {
			return out, fmt.Errorf("historical cache start: %w", err)
		}
		if earliest.Start != nil {
			videoStart = *earliest.Start
		} else {
			videoStart = now.Add(-time.Hour)
		}
	}
	var videoCount struct{ Count int64 }
	if err := db.Raw("SELECT COUNT(*) AS count FROM media_cache WHERE deleted_at IS NULL AND created_at >= ? AND created_at <= ?", videoStart, now).Scan(&videoCount).Error; err != nil {
		return out, fmt.Errorf("historical video count: %w", err)
	}
	out.Videos = videoCount.Count
	if period == PeriodAll {
		out.Historical = out.Videos // backwards-compatible alias for existing widgets
	}
	type videoPlatformCount struct {
		Platform string
		Count    int64
	}
	var videoPlatforms []videoPlatformCount
	if err := db.Raw(`
SELECT split_part(source_key, ':', 1) AS platform, COUNT(*) AS count
FROM media_cache
WHERE deleted_at IS NULL AND created_at >= ? AND created_at <= ?
GROUP BY platform`, videoStart, now).Scan(&videoPlatforms).Error; err != nil {
		return out, fmt.Errorf("historical video platforms: %w", err)
	}
	for _, p := range videoPlatforms {
		for i := range out.VideoPlatforms {
			if out.VideoPlatforms[i].Name == p.Platform {
				out.VideoPlatforms[i].Count = p.Count
			}
		}
	}
	out.VideoBuckets = make([]int64, n)
	videoSpan := now.Sub(videoStart).Seconds() / float64(n)
	if videoSpan < 1 {
		videoSpan = 1
	}
	var videoBucketCounts []struct {
		Idx   int
		Count int64
	}
	if err := db.Raw(`
SELECT LEAST(?, GREATEST(0, FLOOR(EXTRACT(EPOCH FROM (created_at - ?)) / ?)::int)) AS idx,
 COUNT(*) AS count
FROM media_cache
WHERE deleted_at IS NULL AND created_at >= ? AND created_at <= ?
GROUP BY idx ORDER BY idx`, n-1, videoStart, videoSpan, videoStart, now).Scan(&videoBucketCounts).Error; err != nil {
		return out, fmt.Errorf("historical video timeline: %w", err)
	}
	for _, bucket := range videoBucketCounts {
		if bucket.Idx >= 0 && bucket.Idx < n {
			out.VideoBuckets[bucket.Idx] = bucket.Count
		}
	}
	return out, nil
}
