package download

import (
	"bytes"
	"io"
	"strconv"
	"strings"
)

// ProgressEvent describes the currently downloading stream, not overall
// YouTube audio/video merge or Telegram upload completion.
type ProgressEvent struct {
	Percent         int
	Known           bool
	DownloadedBytes int64
	TotalBytes      int64
}

// yt-dlp writes progress to stderr. The exact size is unavailable on some
// extractors, so never derive a numeric percentage from an estimate.
const progressPrefix = "VIDSAVE_PROGRESS|"
const progressTemplate = "download:" + progressPrefix + "%(progress.status)s|%(progress.downloaded_bytes)s|%(progress.total_bytes)s|%(progress.total_bytes_estimate)s"

func parseProgressLine(line string) (ProgressEvent, bool) {
	if !strings.HasPrefix(line, progressPrefix) {
		return ProgressEvent{}, false
	}
	fields := strings.Split(strings.TrimPrefix(line, progressPrefix), "|")
	if len(fields) != 4 || (fields[0] != "downloading" && fields[0] != "finished") {
		return ProgressEvent{}, false
	}
	downloaded, err := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64)
	if err != nil || downloaded < 0 {
		return ProgressEvent{Known: false}, true
	}
	total, err := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)
	if err != nil || total <= 0 {
		// total_bytes_estimate is intentionally not used as an exact measure.
		return ProgressEvent{Known: false, DownloadedBytes: downloaded}, true
	}
	pct := 100
	if downloaded < total {
		// Avoid integer multiplication overflow on enormous/invalid inputs.
		pct = int(float64(downloaded) / float64(total) * 100)
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return ProgressEvent{Percent: pct, Known: true, DownloadedBytes: downloaded, TotalBytes: total}, true
}

// progressWriter handles both \n and \r, bounds diagnostic memory and does not
// block on Telegram networking (callback must never perform remote I/O).
type progressWriter struct {
	pending  []byte
	logs     bytes.Buffer
	callback func(ProgressEvent)
}

func (w *progressWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' || b == '\r' {
			if len(w.pending) != 0 {
				w.line(string(w.pending))
				w.pending = w.pending[:0]
			}
		} else if len(w.pending) < 8192 {
			w.pending = append(w.pending, b)
		}
	}
	return len(p), nil
}

func (w *progressWriter) flush() {
	if len(w.pending) != 0 {
		w.line(string(w.pending))
		w.pending = nil
	}
}

func (w *progressWriter) line(text string) {
	if event, ok := parseProgressLine(text); ok {
		if w.callback != nil {
			w.callback(event)
		}
		return
	}
	// Keep last 64 KiB of stderr for yt-dlp error classification, no unbounded
	// stderr accumulation from a misbehaving extractor.
	const maxLogs = 64 * 1024
	_, _ = io.WriteString(&w.logs, text+"\n")
	if w.logs.Len() > maxLogs {
		tail := append([]byte(nil), w.logs.Bytes()[w.logs.Len()-maxLogs:]...)
		w.logs.Reset()
		_, _ = w.logs.Write(tail)
	}
}

func (w *progressWriter) diagnostic() string {
	w.flush()
	return w.logs.String()
}
