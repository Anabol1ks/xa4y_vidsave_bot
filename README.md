# xa4y_vidsave_bot

Telegram-бот на Go для скачивания публичных коротких видео из **Instagram Reels**, **TikTok** и **YouTube Shorts**.

## Как работает

1. Получает ссылку в личном чате или группе (также из подписи или скрытого Telegram `text_link`).
2. Проверяет домен и формат URL. Поддерживаются `instagram.com/reel/...`, `tiktok.com/@.../video/...`, `vm.tiktok.com/...`, `youtube.com/shorts/...`, `m.youtube.com/shorts/...`, `youtu.be/VIDEO_ID`.
3. Ищет Telegram `file_id` в PostgreSQL по ключу `platform:video_id`. Если видео уже загружено, отправляет его из кэша.
4. В случае промаха запускает `yt-dlp` с ограничением конкурентных загрузок, после скачивания проверяет размер, хеширует файл и отправляет его через Telegram Bot API.
5. Сохраняет `file_id` в PostgreSQL, позволяет поделиться видео через inline-кнопку и удаляет временные файлы.

## YouTube Shorts

- Только индивидуальные ссылки `/shorts/VIDEO_ID` и короткие ссылки `youtu.be/VIDEO_ID`; плейлисты и `/watch` URL не принимаются.
- Не более **180 секунд**. Для `youtu.be` проверяется длительность, поскольку сам формат короткой ссылки не гарантирует, что ролик является Shorts.
- Приоритет — совместимое H.264 MP4-видео (при недоступности — другое MP4) до 1080 пикселей **по ширине** + M4A-аудио; FFmpeg объединяет дорожки в один MP4.
- Для YouTube используются `yt-dlp[default]` и Node.js (JS challenge solver). `yt-dlp` закреплён на версии `2026.10.26` в Dockerfile/Compose и может быть обновлён через `YT_DLP_VERSION`.
- YouTube периодически меняет требования к неавторизованным запросам (включая PO Tokens). Публичные видео, требующие входа, проверки бота или недоступные на IP VPS, могут не скачиваться. Бот не хранит cookies аккаунтов.
- Telegram Bot API разрешает загружать видео до **50 MiB**. Бот ограничивает загрузку и дополнительно проверяет размер файла перед отправкой.

## Развёртывание

Создать `.env` по `.env.example`, задать `BOT_TOKEN`, `DATABASE_URL`, `POSTGRES_PASSWORD` и другие переменные. Не коммитить настоящий `.env`.

```sh
docker compose up -d --build
docker compose ps
docker compose logs --tail=100 bot
```

`.github/workflows/deploy.yml` автоматически обновляет рабочую копию на VPS после пуша в `main`. `.github/workflows/ci.yml` проверяет Go-тесты, `go vet` и Docker-сборку до слияния PR.

```sh
go test ./...
go vet ./...
```

Проверять работу Telegram-бота в production отправкой своей ссылки. Не запускать второй экземпляр с тем же `BOT_TOKEN`: polling-сессии будут конфликтовать.
