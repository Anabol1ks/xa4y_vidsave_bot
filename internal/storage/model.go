package storage

import (
	"time"

	"gorm.io/gorm"
)

// MediaCache — таблица кэша медиа-файлов.
// Хранит только Telegram file_id, без самого файла.
type MediaCache struct {
	ID             uint   `gorm:"primaryKey"`
	SourceKey      string `gorm:"uniqueIndex;size:512;not null"` // platform:video_id (напр. "tiktok:123456")
	SHA256         string `gorm:"index;size:64"`                 // хэш файла для дедупликации
	TgFileID       string `gorm:"size:512;not null"`             // Telegram file_id для повторной отправки
	TgFileUniqueID string `gorm:"size:256;not null"`             // уникальный ID файла в Telegram
	SizeBytes      int64  `gorm:"not null"`
	HitCount       int64  `gorm:"default:0;not null"` // сколько раз отправлен из кэша
	CreatedAt      time.Time
	LastUsedAt     time.Time
	DeletedAt      gorm.DeletedAt `gorm:"index"`
}

// TableName — имя таблицы в БД.
func (MediaCache) TableName() string {
	return "media_cache"
}

// SourceKeyFromParsed формирует source_key из типа и ID.
func SourceKeyFromParsed(linkType, videoID string) string {
	return linkType + ":" + videoID
}
