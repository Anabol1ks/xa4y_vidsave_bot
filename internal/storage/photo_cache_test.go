package storage

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPhotoAlbumCacheValidatesIDs(t *testing.T) {
	for _, ids := range [][]string{nil, {}, {""}, make([]string, 36), {strings.Repeat("x", 1025)}} {
		if validatePhotoIDs(ids) == nil {
			t.Fatal("accepted invalid IDs")
		}
	}
	if err := validatePhotoIDs([]string{"TelegramFileID1", "TelegramFileID2"}); err != nil {
		t.Fatal(err)
	}
}

func TestPhotoAlbumCachePostgres(t *testing.T) {
	dsn := os.Getenv("VIDSAVE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL test DB")
	}
	u, err := url.Parse(dsn)
	if err != nil || !strings.Contains(u.Path, "test") {
		t.Fatal("must use disposable test database")
	}
	schema := "vidsave_photo_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	root, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := root.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := root.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer root.Exec("DROP SCHEMA " + schema + " CASCADE")
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	store, err := New(u.String(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := "tiktok-photo:7706556456627096852"
	if _, err := store.LookupPhotoAlbum(key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty cache: %v", err)
	}
	ids := []string{"cached-photo-1", "cached-photo-2"}
	if err := store.SavePhotoAlbum(key, ids); err != nil {
		t.Fatal(err)
	}
	first, err := store.LookupPhotoAlbum(key)
	if err != nil || len(first) != 2 || first[0] != ids[0] {
		t.Fatalf("cache value incorrect: %v %+v", err, first)
	}
	if err := store.SavePhotoAlbum(key, []string{"updated-photo"}); err != nil {
		t.Fatal(err)
	}
	second, err := store.LookupPhotoAlbum(key)
	if err != nil || len(second) != 1 || second[0] != "updated-photo" {
		t.Fatalf("upsert did not replace ids: %+v %v", second, err)
	}
	var count int64
	if err := store.db.Model(&MediaCache{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("photo cache polluted video history: count=%d", count)
	}
}
