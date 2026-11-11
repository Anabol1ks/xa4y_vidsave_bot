package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"xa4yy_vidsave/internal/config"
)

func TestDownloadGateActiveAndCooldown(t *testing.T) {
	clock := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	guard := newDownloadGate(&config.Config{
		DownloadUserCooldown:   3 * time.Second,
		DownloadSourceCooldown: 30 * time.Second,
	})
	guard.now = func() time.Time { return clock }
	release, err := guard.begin(1, 1, "youtube:x", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.begin(1, 1, "youtube:x", false); !errors.Is(err, ErrDownloadBusy) {
		t.Fatal("duplicate while running accepted")
	}
	if _, err := guard.begin(1, 1, "tiktok:y", false); !errors.Is(err, ErrDownloadBusy) {
		t.Fatal("concurrent distinct request accepted")
	}
	release()
	release()
	clock = clock.Add(4 * time.Second)
	if _, err := guard.begin(1, 1, "youtube:x", false); !errors.Is(err, ErrDownloadBusy) {
		t.Fatal("same source cooldown bypassed")
	}
	release2, err := guard.begin(1, 1, "tiktok:y", false)
	if err != nil {
		t.Fatal(err)
	}
	release2()
	clock = clock.Add(31 * time.Second)
	release3, err := guard.begin(1, 1, "youtube:x", false)
	if err != nil {
		t.Fatal(err)
	}
	release3()
}

func TestDownloadGateGroupLimit(t *testing.T) {
	clock := time.Now().UTC()
	guard := newDownloadGate(&config.Config{
		DownloadGroupPerMinute: 2,
		DownloadUserCooldown:   time.Second,
		DownloadSourceCooldown: time.Second,
	})
	guard.now = func() time.Time { return clock }
	for i := int64(1); i <= 2; i++ {
		release, err := guard.begin(i, -123, "instagram:video", true)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	if _, err := guard.begin(3, -123, "youtube:other", true); !errors.Is(err, ErrDownloadBusy) {
		t.Fatal("chat quota not enforced")
	}
	clock = clock.Add(61 * time.Second)
	release, err := guard.begin(3, -123, "youtube:other", true)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestDownloadQueueBoundedAndTimeout(t *testing.T) {
	bot := &Bot{
		cfg:               &config.Config{DownloadQueueTimeout: 55 * time.Millisecond},
		downloadSlots:     make(chan struct{}, 1),
		downloadWaitSlots: make(chan struct{}, 1),
	}
	bot.downloadWaitSlots <- struct{}{}
	if _, err := bot.reserveDownload(); !errors.Is(err, ErrDownloadBusy) {
		t.Fatalf("queue overflow not rejected: %v", err)
	}
	<-bot.downloadWaitSlots
	release, err := bot.reserveDownload()
	if err != nil {
		t.Fatal(err)
	}
	bot.downloadSlots <- struct{}{}
	called := false
	start := time.Now()
	err = bot.stagedDownload(context.Background(), func() error { called = true; return nil })
	if !errors.Is(err, ErrDownloadBusy) || called {
		t.Fatalf("queue wait not bounded: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("queue wait exceeded bound")
	}
	release()
	<-bot.downloadSlots
	release, err = bot.reserveDownload()
	if err != nil {
		t.Fatal(err)
	}
	err = bot.stagedDownload(context.Background(), func() error { called = true; return nil })
	if err != nil || !called {
		t.Fatal("free slot not acquired", err)
	}
	release()
	if len(bot.downloadSlots) != 0 || len(bot.downloadWaitSlots) != 0 {
		t.Fatal("semaphore leak")
	}
}

func TestBusyNotificationThrottling(t *testing.T) {
	now := time.Now()
	g := newDownloadGate(nil)
	g.now = func() time.Time { return now }
	if !g.shouldNotify(100) {
		t.Fatal("first message suppressed")
	}
	if g.shouldNotify(100) {
		t.Fatal("burst would spam chat")
	}
	if !g.shouldNotify(200) {
		t.Fatal("independent chat suppressed")
	}
	now = now.Add(11 * time.Second)
	if !g.shouldNotify(100) {
		t.Fatal("notification did not recover")
	}
}
