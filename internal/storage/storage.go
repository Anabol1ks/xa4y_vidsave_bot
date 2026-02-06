package storage

import (
	"errors"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var ErrNotFound = errors.New("cache entry not found")

// Storage — обёртка над GORM для работы с кэшем.
type Storage struct {
	db  *gorm.DB
	log *zap.Logger
}

// New подключается к PostgreSQL, выполняет AutoMigrate и возвращает Storage.
func New(dsn string, log *zap.Logger) (*Storage, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
	})
	if err != nil {
		return nil, err
	}

	// Настраиваем пул соединений
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)

	// AutoMigrate — создаёт/обновляет таблицу
	if err := db.AutoMigrate(&MediaCache{}); err != nil {
		return nil, err
	}

	log.Info("storage initialized (PostgreSQL)")
	return &Storage{db: db, log: log}, nil
}

// Close закрывает соединение с БД.
func (s *Storage) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// --- Операции с кэшем ---

// Lookup ищет запись по source_key. Если найдена — обновляет last_used_at и hit_count.
func (s *Storage) Lookup(sourceKey string) (*MediaCache, error) {
	var entry MediaCache
	result := s.db.Where("source_key = ?", sourceKey).First(&entry)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, result.Error
	}

	// Обновляем статистику использования
	s.db.Model(&entry).Updates(map[string]interface{}{
		"last_used_at": time.Now(),
		"hit_count":    gorm.Expr("hit_count + 1"),
	})

	return &entry, nil
}

// LookupBySHA256 ищет запись по хэшу файла (дедупликация).
func (s *Storage) LookupBySHA256(hash string) (*MediaCache, error) {
	var entry MediaCache
	result := s.db.Where("sha256 = ?", hash).First(&entry)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, result.Error
	}
	return &entry, nil
}

// Save сохраняет новую запись в кэш.
func (s *Storage) Save(entry *MediaCache) error {
	entry.CreatedAt = time.Now()
	entry.LastUsedAt = time.Now()
	return s.db.Create(entry).Error
}

// Upsert — вставка или обновление по source_key.
func (s *Storage) Upsert(entry *MediaCache) error {
	var existing MediaCache
	result := s.db.Where("source_key = ?", entry.SourceKey).First(&existing)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return s.Save(entry)
		}
		return result.Error
	}

	// Обновляем существующую запись
	return s.db.Model(&existing).Updates(map[string]interface{}{
		"sha256":            entry.SHA256,
		"tg_file_id":        entry.TgFileID,
		"tg_file_unique_id": entry.TgFileUniqueID,
		"size_bytes":        entry.SizeBytes,
		"last_used_at":      time.Now(),
	}).Error
}
