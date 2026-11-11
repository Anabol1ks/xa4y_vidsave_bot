package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxPhotoAlbumCacheSize = 35

// PhotoAlbumCache is separate from media_cache: the latter stores one video
// file_id, and historical video analytics must not start counting images.
type PhotoAlbumCache struct {
	SourceKey   string    `gorm:"primaryKey;size:100"`
	FileIDsJSON string    `gorm:"type:text;not null"`
	CreatedAt   time.Time `gorm:"not null"`
	LastUsedAt  time.Time `gorm:"not null"`
	HitCount    int64     `gorm:"not null;default:0"`
}

func validatePhotoIDs(ids []string) error {
	if len(ids) < 1 || len(ids) > maxPhotoAlbumCacheSize {
		return fmt.Errorf("invalid photo album count: %d", len(ids))
	}
	for _, id := range ids {
		if id == "" || len(id) > 1024 {
			return errors.New("invalid Telegram photo file ID")
		}
	}
	return nil
}

func (s *Storage) LookupPhotoAlbum(sourceKey string) ([]string, error) {
	var row PhotoAlbumCache
	err := s.db.Where("source_key = ?", sourceKey).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal([]byte(row.FileIDsJSON), &ids); err != nil {
		return nil, err
	}
	if err := validatePhotoIDs(ids); err != nil {
		return nil, err
	}
	// Counter is a convenience metric; cache hit should not fail if its update fails.
	_ = s.db.Model(&PhotoAlbumCache{}).Where("source_key = ?", sourceKey).
		Updates(map[string]interface{}{
			"last_used_at": time.Now().UTC(),
			"hit_count":    gorm.Expr("hit_count + 1"),
		}).Error
	return ids, nil
}

func (s *Storage) SavePhotoAlbum(sourceKey string, ids []string) error {
	if sourceKey == "" || len(sourceKey) > 100 {
		return errors.New("invalid photo album cache key")
	}
	if err := validatePhotoIDs(ids); err != nil {
		return err
	}
	payload, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	row := PhotoAlbumCache{SourceKey: sourceKey, FileIDsJSON: string(payload), CreatedAt: now, LastUsedAt: now}
	return s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "source_key"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"file_ids_json": string(payload),
			"last_used_at":  now,
		}),
	}).Create(&row).Error
}
