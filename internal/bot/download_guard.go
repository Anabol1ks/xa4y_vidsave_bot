package bot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"xa4yy_vidsave/internal/config"
)

var ErrDownloadBusy = errors.New("download request rate limited or busy")

// downloadGate is in-memory, per-bot-instance abuse protection for Stage 3.
// It is enabled only by DOWNLOAD_WIDGET_ENABLED; legacy mode is unchanged.
type downloadGate struct {
	mu             sync.Mutex
	now            func() time.Time
	active         map[int64]struct{}
	lastUser       map[int64]time.Time
	lastSource     map[string]time.Time
	lastDenial     map[int64]time.Time
	groupTimes     map[int64][]time.Time
	userInterval   time.Duration
	sourceCooldown time.Duration
	groupPerMinute int
}

func newDownloadGate(cfg *config.Config) *downloadGate {
	userInterval := 3 * time.Second
	sourceCooldown := 30 * time.Second
	groupPerMinute := 20
	if cfg != nil {
		if cfg.DownloadUserCooldown > 0 {
			userInterval = cfg.DownloadUserCooldown
		}
		if cfg.DownloadSourceCooldown > 0 {
			sourceCooldown = cfg.DownloadSourceCooldown
		}
		if cfg.DownloadGroupPerMinute > 0 {
			groupPerMinute = cfg.DownloadGroupPerMinute
		}
	}
	return &downloadGate{
		now:            time.Now,
		active:         make(map[int64]struct{}),
		lastUser:       make(map[int64]time.Time),
		lastSource:     make(map[string]time.Time),
		lastDenial:     make(map[int64]time.Time),
		groupTimes:     make(map[int64][]time.Time),
		userInterval:   userInterval,
		sourceCooldown: sourceCooldown,
		groupPerMinute: groupPerMinute,
	}
}

// begin rejects repeated requests before using network, DB cache or yt-dlp.
// The returned release must be called exactly once, including on cache hits.
func (g *downloadGate) begin(userID, chatID int64, sourceKey string, group bool) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	principal := userID
	if principal <= 0 {
		principal = chatID
	}
	now := g.now()
	source := fmt.Sprintf("%d:%s", principal, sourceKey)
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, active := g.active[principal]; active {
		return nil, ErrDownloadBusy
	}
	if last, ok := g.lastUser[principal]; ok && now.Sub(last) < g.userInterval {
		return nil, ErrDownloadBusy
	}
	if last, ok := g.lastSource[source]; ok && now.Sub(last) < g.sourceCooldown {
		return nil, ErrDownloadBusy
	}
	if group {
		records := g.groupTimes[chatID]
		valid := records[:0]
		for _, t := range records {
			if now.Sub(t) < time.Minute {
				valid = append(valid, t)
			}
		}
		if len(valid) >= g.groupPerMinute {
			g.groupTimes[chatID] = valid
			return nil, ErrDownloadBusy
		}
		g.groupTimes[chatID] = append(valid, now)
	}

	g.active[principal] = struct{}{}
	g.lastUser[principal] = now
	g.lastSource[source] = now
	// Bound memory growth from one-off senders and unique URLs.
	if len(g.lastSource) > 10000 {
		for k, t := range g.lastSource {
			if now.Sub(t) >= g.sourceCooldown {
				delete(g.lastSource, k)
			}
		}
	}
	if len(g.lastUser) > 10000 {
		for k, t := range g.lastUser {
			if now.Sub(t) >= time.Minute {
				delete(g.lastUser, k)
			}
		}
	}
	if len(g.groupTimes) > 10000 {
		for k, times := range g.groupTimes {
			if len(times) == 0 || now.Sub(times[len(times)-1]) >= time.Minute {
				delete(g.groupTimes, k)
			}
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			delete(g.active, principal)
			ended := g.now()
			g.lastUser[principal] = ended
			g.lastSource[source] = ended
			g.mu.Unlock()
		})
	}, nil
}

// shouldNotify refuses to spam busy messages back to a chat under attack.
func (g *downloadGate) shouldNotify(chatID int64) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if last, ok := g.lastDenial[chatID]; ok && now.Sub(last) < 10*time.Second {
		return false
	}
	g.lastDenial[chatID] = now
	if len(g.lastDenial) > 10000 {
		for k, t := range g.lastDenial {
			if now.Sub(t) >= time.Minute {
				delete(g.lastDenial, k)
			}
		}
	}
	return true
}

// reserveDownload runs BEFORE sending a widget, bounding active status
// sessions as well as the number of queued yt-dlp operations.
func (b *Bot) reserveDownload() (func(), error) {
	select {
	case b.downloadWaitSlots <- struct{}{}:
		return func() { <-b.downloadWaitSlots }, nil
	default:
		return nil, ErrDownloadBusy
	}
}

// stagedDownload is called only with a held reservation. Queue waiting is
// time-bounded and the active yt-dlp semaphore is always released.
func (b *Bot) stagedDownload(ctx context.Context, cb func() error) error {
	waitCtx, cancelWait := context.WithTimeout(ctx, b.cfg.DownloadQueueTimeout)
	defer cancelWait()
	select {
	case b.downloadSlots <- struct{}{}:
		defer func() { <-b.downloadSlots }()
	case <-waitCtx.Done():
		return fmt.Errorf("%w: queue wait exceeded", ErrDownloadBusy)
	}
	return cb()
}
