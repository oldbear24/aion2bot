# Deploying the bot

Every release is published as a Docker image for `linux/amd64` and `linux/arm64`:

```
ghcr.io/oldbear24/aion2bot:latest     newest release
ghcr.io/oldbear24/aion2bot:1          newest 1.x release
ghcr.io/oldbear24/aion2bot:1.0.0      exactly this release
```

The image holds only the bot and `game.json`. The bot's data (linked servers, posts, profiles) is one SQLite database in the `/data` volume.

> Run **one** copy of the bot per Discord token. Two copies would both answer every click and fight over the posts.

## 1. Before you start

- A Linux server or VPS with [Docker Engine and the Compose plugin](https://docs.docker.com/engine/install/). 512 MB of RAM is plenty.
- The bot's token from the [Discord Developer Portal](https://discord.com/developers/applications) → your app → **Bot**.
- The bot invited to your servers. See the invite link in [README.md](README.md#setup).

The image is public, so no `docker login` is needed.

## 2. Start it

Make a folder on the server and download [`compose.yaml`](compose.yaml) into it:

```sh
mkdir aion2bot && cd aion2bot
curl -fsSLO https://raw.githubusercontent.com/oldbear24/aion2bot/main/compose.yaml
```

Create `.env` next to it:

```sh
DISCORD_TOKEN=your-bot-token
# Optional: pin a release instead of following latest.
# AION2BOT_VERSION=1.0.0
```

Keep the file private (`chmod 600 .env`): the token controls the bot. Then:

```sh
docker compose up -d
docker compose logs -f      # "AION 2 LFG bot v1.0.0 is running …" means it's up
```

Then run `/lfg-setup` on each server (see README.md). Slash commands are registered globally on start.

### Without Compose

```sh
docker volume create aion2bot-data
docker run -d --name aion2bot --restart unless-stopped \
  --env-file .env -v aion2bot-data:/data \
  ghcr.io/oldbear24/aion2bot:latest
```

## 3. Configuration

All settings are environment variables in `.env`:

| Variable | Default in the image | Meaning |
|---|---|---|
| `DISCORD_TOKEN` | (required) | The bot's token |
| `DEV_GUILD_ID` | empty | Register commands on this one test server only. Leave empty in production. |
| `GAME_FILE` | `/app/game.json` | Dungeons, modes, GS requirements, roles and classes |
| `DB_FILE` | `/data/aion2bot.db` | The SQLite database |

To change dungeons or GS requirements without waiting for a release, put your own `game.json` next to `compose.yaml`
and uncomment the `./game.json:/app/game.json:ro` line in `compose.yaml`. A restart picks it up.

## 4. Updating

```sh
docker compose pull && docker compose up -d
```

The database is upgraded automatically on start. With `AION2BOT_VERSION` pinned, change it in `.env` first.
To go back, set `AION2BOT_VERSION` to the previous version and run the same command. Upgrades only add to the database, so an older version still starts on it.

## 5. Backups

The database is small. Stop the bot for a moment so the copy is consistent:

```sh
docker compose stop
docker run --rm -v aion2bot_data:/data -v "$PWD":/backup alpine \
  sh -c 'cp /data/aion2bot.db /backup/aion2bot-$(date +%F).db'
docker compose start
```

To restore, stop the bot, copy a backup back as `/data/aion2bot.db` the same way, and start it again.
Compose names the volume `<folder>_data`, so `aion2bot_data` for a folder named `aion2bot` (`docker volume ls` shows it).

## 6. Moving from the Windows .exe

1. Stop the old bot with **Ctrl+C** in its window, so it saves everything to `data\aion2bot.db`.
2. Copy `data\aion2bot.db` to the server, next to `compose.yaml`. If `aion2bot.db-wal` is still there, copy it too.
3. Load it into the volume and hand it to the bot's user (65532):

   ```sh
   docker compose create
   docker run --rm -v aion2bot_data:/data -v "$PWD":/backup alpine \
     sh -c 'cp /backup/aion2bot.db* /data/ && chown 65532:65532 /data/aion2bot.db*'
   docker compose up -d
   ```

Linked servers, open posts and profiles carry over. Use the same `DISCORD_TOKEN` as before.

## 7. Releasing a new version

For maintainers. CI tests every push to `main` and every pull request, and builds the image without publishing it.
To release, tag a commit on `main`:

```sh
git tag v1.1.0
git push origin v1.1.0
```

The [Release workflow](.github/workflows/release.yml) then:

1. runs `go vet` and the tests,
2. builds the image for amd64 and arm64 and pushes `1.1.0`, `1.1`, `1` and `latest` to `ghcr.io/oldbear24/aion2bot`,
3. creates a GitHub release with the changes since the previous tag.

Use [semantic versions](https://semver.org): bump the last number for fixes, the middle one for new features, the first one for changes that need admins to do something (new permissions, removed commands).
A tag like `v1.1.0-rc.1` publishes only `1.1.0-rc.1`, without moving `latest`.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `DISCORD_TOKEN is not set` | `.env` is missing or not next to `compose.yaml` |
| `Authentication failed` / 4004 in the log | The token was reset in the Developer Portal. Put the new one in `.env` and run `docker compose up -d` |
| `unable to open database file` | The volume isn't writable by user 65532. Run the `chown` command from section 6 |
| `denied` or `not found` when pulling | Check the image name and tag against the [releases](https://github.com/oldbear24/aion2bot/releases) |
| Pings arrive as DMs instead of in a thread | The bot lacks thread permissions in the LFG channel. Run `/lfg-check` |
| `The Message Content intent is off` in the log; thread messages aren't relayed to other servers | Turn on **Message Content Intent** under **Bot** in the Developer Portal, then `docker compose restart` |
| Relayed thread messages come from the bot instead of under the player's name | The bot lacks **Manage Webhooks** in that server's LFG channel. Run `/lfg-check` |
| Roles and classes show plain emojis instead of icons | The icon upload failed at startup; the log says why. They're uploaded again on the next start |
