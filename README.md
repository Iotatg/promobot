# Promobot

Promobot is an owner-only Telegram **scheduled announcement manager** built in Go
with [gotdbot](https://github.com/Iotatg/gotdbot) (a pure-Go TDLib wrapper) and
MongoDB. It posts recurring announcements to **channels and groups that you own or
administer**, on a schedule you define.

It is intentionally scoped to legitimate automation:

- The controller is a **Bot API** bot, usable only by the owner (`OWNER_ID`).
- An optional **assistant** (your own account, logged in via a TDLib session or
  an imported Pyrogram StringSession) can post as you to chats where that
  account is an admin.
- A channel can only be registered if the sending account is an **admin** of it.
- **No** auto-joining of groups, **no** unsolicited messaging to chats you do not
  own, **no** multi-account farms or credit systems.

## Features

- Animated `/start` boot sequence rendered by editing a single message.
- Inline-button main menu: add channel, list channels, manage scheduled posts.
- Per-channel scheduling with randomized jitter and warmup delay.
- **Broadcast** to all registered channels (text or a replied-to post), FloodWait
  aware, with progress reporting.
- **Media attachments** (photo, video, audio, document, animation) in scheduled
  posts and broadcasts.
- **Mood-based sticker packs**: save stickers under moods and send them on demand.
- FloodWait handling: pauses the affected channel for the required time, without
  crashing others.
- Optional structured logging to a private `LOG_GROUP_ID`.
- MongoDB persistence for channels, schedules, and sticker packs.

## Requirements

- Go 1.27.1+ (the module auto-downloads the toolchain if needed).
- A `libtdjson` shared library. The easiest way to fetch a prebuilt one:

  ```bash
  go run github.com/Iotatg/gotdbot/scripts/tools
  ```

- MongoDB (local or remote).
- A bot token from [@BotFather](https://t.me/BotFather).
- Your numeric Telegram `API_ID` / `API_HASH` from https://my.telegram.org.

## Setup

1. Install dependencies and the TDLib library:

   ```bash
   go mod tidy
   go run github.com/Iotatg/gotdbot/scripts/tools
   ```

2. Create your `.env`:

   ```bash
   cp .env.example .env
   ```

   Fill in `API_ID`, `API_HASH`, `BOT_TOKEN`, `OWNER_ID`, and `MONGO_URI`.

3. Run it:

   ```bash
   go run ./cmd/promobot
   ```

   On the first run with `ASSISTANT_PHONE` set, the process prompts in the
   terminal for the login code and (if enabled) the 2FA password. The encrypted
   TDLib session is then reused automatically on later runs.

   Alternatively, set `ASSISTANT_SESSION` to a **Pyrogram StringSession** to log
   the assistant in over MTProto (via gotd) with no interactive prompt. The
   session string is imported directly and the account posts as itself.

## Owner commands

| Command | Description |
| --- | --- |
| `/start` | Boot animation and main menu |
| `/addchannel <chat_id\|@username> [bot\|assistant]` | Register a chat you admin |
| `/channels` | List registered channels (with delete buttons) |
| `/newpost` | Create a scheduled announcement (guided, optional media) |
| `/posts` | List, toggle or delete scheduled posts |
| `/broadcast [text] [-silent]` | Send to all registered channels (or reply to a post) |
| `/addsticker <mood>` | Save the replied sticker to a mood pack |
| `/sticker <mood>` | Send a random sticker from a mood pack |
| `/stickerpacks` | List mood packs |
| `/clearstickers <mood>` | Delete a mood pack |
| `/id` | Show your user id and the current chat id |
| `/help` | Help text |

During `/newpost`, send `/cancel` at any step to abort.

Broadcast only targets channels in your allowlist (chats the sending account
admins). It never messages arbitrary users or dialogs.

## How scheduling works

- Each schedule posts to one registered channel at a fixed interval (minutes).
- A random jitter of `0..JITTER_SECONDS` is added to every interval.
- Newly added channels wait `WARMUP_MINUTES` before their first send.
- On a TDLib `FloodWait`, the affected channel sleeps for the reported duration
  (capped at `MAX_FLOODWAIT_SECONDS`), then resumes. Other channels are unaffected.
- Sends are dispatched through the channel's configured sender (bot or assistant).

## Project layout

```
cmd/promobot         entrypoint and client wiring
internal/config      .env loading and validation
internal/store       MongoDB persistence (channels, schedules, sticker packs)
internal/media       media sending (photo/video/audio/document/animation)
internal/notify      slog + Telegram status logging
internal/scheduler   per-schedule goroutines, jitter, warmup, FloodWait
internal/bot         controller bot: menus, commands, broadcast, stickers
```

## Notes on safe use

- Only register channels where the sending account is an admin.
- Keep `JITTER_SECONDS` and intervals reasonable to avoid rate limits.
- Protect `.env` and the `.tdlib/` session directories: they grant access to the
  logged-in account.