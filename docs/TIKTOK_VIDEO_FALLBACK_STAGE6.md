# Stage 6 — TikTok video multi-extractor fallback

## Scope and rollout

- Primary downloader remains yt-dlp. Pin updated to 2026.11.23 (Dockerfile, Compose and example env); any existing YT_DLP_VERSION override must be reviewed before release. gallery-dl remains pinned at 1.32.19.
- TIKTOK_VIDEO_FALLBACK_ENABLED is false by default; set true only after CI/production smoke validation. No new external services, credentials, accounts or proxy changes.
- Only explicit TikTok webpage extraction failures trigger one gallery-dl attempt. No fallback for confirmed audience, login, private, IP/region, rate-limit, network, timeout, unsupported or unknown errors. No access restrictions bypassed.
- Only direct HTTPS TikTok video URLs or safely resolved short links; photos, profiles and unrelated domains are rejected. The existing photo handler stays separate.
- The attempt uses the same download slot, overall deadline and request record; an extra cap of 45 seconds, no retry by gallery-dl, actual byte ceiling of min(MAX_DOWNLOAD_MB, 50 MiB), a temporary directory, regular MP4 only and ffprobe video-stream validation. Files are removed on failure or after normal sending.
- Progress becomes indeterminate during fallback, using existing widget/text status. No duplicate status or analytics request.
- Rollback: set TIKTOK_VIDEO_FALLBACK_ENABLED=false and redeploy; the existing primary remains available. Telegram HTML widget availability is controlled by Telegram, not this feature.

## VPS evidence (2026-11-25)

Using an isolated Python environment, not production:
- Public TikTok @patroxofficial/video/6742501081818877190: yt-dlp 2026.11.23 reported Unexpected response from webpage request; gallery-dl 1.32.19 downloaded a 2,748,647-byte MP4; PyAV confirmed H.264, AAC, 540x960, 27.47 seconds and decoded a video frame.
- Public TikTok @hankgreen1/video/7047596209028074758: primary metadata and gallery-dl metadata both succeeded.
- Reported vt.tiktok.com/ZSs6HJd2f: 2026.11.23 explicitly identified audience restrictions requiring login; no fallback is permitted for that diagnosis.
- Instagram Reels DbJLODStVAd and previously reported DfCnUaROiyg: metadata extraction succeeded. YouTube Shorts jNQXAC9IVRw: production format selector returned MP4 133+140. Not full Telegram delivery tests.

## Automated coverage

Mocked extractors test successful primary/fallback, terminal access errors, single attempt, invalid/oversized/symlink files, cleanup, cancellation and safe redirect resolution. A CI PostgreSQL + mock Telegram test exercises single delivery, cache and single successful analytics event. Real production Telegram E2E remains a release gate.
