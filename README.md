# AION 2 Raid LFG Bot

A Discord bot (Go, [discordgo](https://github.com/bwmarrin/discordgo)) for AION 2 (global) players who are short on party members.
A leader fills in a short panel, and the post appears in the LFG channel of **every server linked with theirs**.
Players join from whichever server they're on, and all copies of the post stay in sync.

It uses Discord's new components:

- **Components V2 messages** (Container, Section, Text Display, Separator): the `/lfg` panel, the post card, the manage panel.
- **Modals with select menus** (Label component): the join form, `/profile`, and the gear score, time & note form.

## How it works

1. **Set up each server.** An admin runs `/lfg-setup channel:#raid-lfg ping_role:@Raiders` (the ping role is optional).
   The bot checks its own permissions in that channel and the ping role, and shows a ✅/⚠️/❌ checklist. `/lfg-check` shows the same checklist at any time.
2. **Link servers.** Posts are only shared between linked servers. One admin runs `/lfg-network create name:My Legion` and gets a join code;
   admins of the other servers run `/lfg-network join code:K7M2QX9P`. A server that isn't linked only sees its own posts.
3. **Post a run.** A leader runs `/lfg` and gets a private panel:
   - **Dungeon or raid**, then **Difficulty**: only that dungeon's own modes, e.g. Exploration / Conquest / Conquest [Hard].
   - **Looking for**: roles (🟦 Tank, 🟩 Healer, 🟥 DPS), specific classes, *Any class*, or a mix.
   - **Missing players**: limited by the mode's party size.
   - **Gear score, time & note** (all optional). The game's GS requirement for the mode is shown as a hint.
     The start is typed like `21.09.2026 10:15`, `21.9. 10:15` or just `10:15` (the next 10:15), in the leader's time zone.
     On the post it becomes a Discord timestamp, so every player sees it in their own local time with a countdown.
     A run planned for later expires `post_ttl_hours` after its start, not after it was posted.
   - **Post** sends it to every linked server. New posts ping each server's ping role.
   A player can have up to `max_runs_per_leader` open runs at once.
4. **Join.** Players click **Join**, pick their role and class (only combinations the post asks for) and enter their gear score.
   `/profile` saves a main role, class and GS so the form is filled in; the GS also updates every time they join.
   It also sets the player's time zone for typed start times. Until they pick one, it's guessed from their Discord language (Czech → Prague time).
5. **Manage.** The leader clicks **Manage** on any copy of the post to accept or decline players, adjust the missing count when someone joins
   outside the bot, **Close** the post, or **Delete** it from every server right away.
6. **Pings.** The leader and the players are linked with @mentions on every linked server they're a member of, so they're easy to find and message.
   Where they aren't a member, they show by name, since Discord can't resolve the mention there.
   Everyone is pinged **once** per run, in a thread the bot starts under the post: the leader when the first player joins, each player when they're accepted.
   The channel itself only holds the posts. A server's thread is started with its first ping, so posts nobody joins don't get one.
   After that, changes only edit the card, which never notifies anyone. If a server has no copy of the post, or the bot can't use threads there, a DM is sent instead.
7. **Cleanup.** Posts expire after `post_ttl_hours`. A closed or expired post is deleted from every server, with the thread under it, 24 hours after it ended.
   The bot can delete threads it started; if Discord ever refuses, it deletes its pings and archives the thread instead.

## Languages

The bot speaks **English** and **Czech** (čeština):

- Private messages (the `/lfg` panel, Manage, forms, `/help`, errors) follow each user's Discord language, so players with Czech Discord see Czech automatically.
- Public posts, and the pings in the threads under them, follow the server's language, set with `/lfg-setup language:`. If you don't pick one, it comes from the server's locale, or else the admin's language. One run can show in Czech on one server and in English on another.
- Slash command descriptions are in both languages.
- Game names stay as in the global client: dungeons, modes (Conquest [Hard]), classes and roles (Tank, Healer, DPS).

All texts live in `internal/i18n` (`en.go`, `cs.go`); a test checks both catalogs have the same messages and placeholders.

## Setup

1. In the [Discord Developer Portal](https://discord.com/developers/applications), create an application, open **Bot** and copy the token. No privileged intents are needed.
2. Invite the bot to each server (replace `APP_ID`):

   ```
   https://discord.com/oauth2/authorize?client_id=APP_ID&scope=bot+applications.commands&permissions=309237648384
   ```

   `309237648384` is all the bot needs: View Channel and Send Messages to post, Create Public Threads and Send Messages in Threads to ping in a thread under each post.
   Use `309237779456` to also add *Mention @everyone, @here and All Roles*, which lets the bot ping a ping role that isn't set as mentionable.
   Nothing else is needed: the bot only edits and deletes its own messages and threads, and buttons, forms and commands work through interactions.
   Most servers give the thread permissions to @everyone, so older invites keep working; `/lfg-check` shows if one is missing.
3. Run the released Docker image on a server: see **[DEPLOY.md](DEPLOY.md)**. In short:

   ```sh
   echo DISCORD_TOKEN=your-bot-token > .env
   docker compose up -d        # with compose.yaml from this repository
   ```

   Or build and run it locally while developing:

   ```powershell
   copy .env.example .env      # then put your token in DISCORD_TOKEN
   go build -o aion2bot.exe . ; .\aion2bot.exe
   ```

   While testing, set `DEV_GUILD_ID` to your test server's ID so command changes show up immediately. Leave it empty in production.

## Releases

Pushing a tag like `v1.1.0` runs the tests, publishes `ghcr.io/oldbear24/aion2bot:1.1.0` (also `1.1`, `1` and `latest`, for amd64 and arm64)
and creates a GitHub release. CI tests every push and pull request. Details are in [DEPLOY.md](DEPLOY.md#7-releasing-a-new-version).

## Commands

| Command | Who | What it does |
|---|---|---|
| `/lfg` | everyone | Opens the panel to post a run to all linked servers |
| `/profile` | everyone | Saves your main role, class, gear score and time zone |
| `/help` | everyone | Shows how to use the bot, only to you (members with Manage Server also see the admin commands) |
| `/help-public` | Manage Messages | Posts the player guide (without the admin commands) in the channel for everyone (permission adjustable in Server Settings → Integrations) |
| `/lfg-setup channel [ping_role] [language]` | Manage Server | Sets this server's LFG channel, ping role and post language, with a permission checklist |
| `/lfg-check [channel]` | Manage Server | Checks the bot's permissions, the ping role and the network |
| `/lfg-network create name` | Manage Server | Starts a network and shows its join code |
| `/lfg-network join code` | Manage Server | Links this server to a network |
| `/lfg-network info` | Manage Server | Shows the network, its code and its servers |
| `/lfg-network leave` | Manage Server | Stops sharing posts with the network |
| `/lfg-network remove server` | Network owner | Removes a server from the network |
| `/lfg-network new-code` | Network owner | Replaces the join code |
| `/lfg-disable` | Manage Server | Stops new posts from arriving on this server |

## Game data (`game.json`)

```jsonc
{
  "post_ttl_hours": 3,        // open posts expire after this many hours
  "max_missing": 9,           // highest "missing players" option (up to 25)
  "max_runs_per_leader": 5,   // open runs one player can have at once
  "dungeons": [               // 1–25 entries
    { "id": "krao-cave", "name": "Krao Cave", "emoji": "🗺️",
      "party_size": 5,                          // caps "missing players" at 4
      "image": "https://…/krao.png",            // optional thumbnail on the post
      "description": "Expedition",  // shown in the menu; party size and GS range are added in the reader's language
      "modes": [                                // difficulties, easiest first
        { "id": "exploration", "name": "Exploration", "gs": 200 },
        { "id": "conquest-hard", "name": "Conquest [Hard]", "gs": 2600 }
        // a mode can set its own "party_size" if it differs from the dungeon's
      ] }
  ],
  "roles": [                  // roles leaders can look for; the emoji marks players on the post
    { "id": "tank", "name": "Tank", "emoji": "🟦" }
  ],
  "classes": [                // "Any class" is added automatically
    { "id": "gladiator", "name": "Gladiator", "emoji": "⚔️", "roles": ["tank", "dps"] }
  ]
}
```

The list covers what is live on the **global** version as of 31 October 2026:
- **Expedition (5 players):** Krao Cave, Draupnir, Urugugu Canyon, Fire Temple, Vakron Sky Island, Ferocious Horn Den. Exploration / Conquest, plus Conquest [Hard] on Krao Cave and Draupnir (GS 2,600) and on Urugugu Canyon and Vakron Sky Island (GS 2,700, since 28 October).
- **Transcendence (5 players):** Deus Research Base, Shattered Arkanis. Stages 1–4.
- **Subjugation (4 players):** Dredgion Depot. Easy / Normal / Hard.
- **Sanctuary raid (10 players):** Abyssal Forge: Ludra. Normal (GS 2,800) / Hard (GS 3,000), both open from 4 November.

Content only in the client files (Chalice of Muspel, Corroded Decontamination Facility, other hard modes) is left out; add it when it opens on global.
Class roles: Templar tanks, Gladiator tanks or DPS, Cleric heals, Chanter heals (support) or DPS, the other four are DPS.
Emojis can be Unicode or custom (`"<:krao:123456789012345678>"`). Keep `id`s stable: open posts refer to them.

## Data

Everything is stored in SQLite at `data/aion2bot.db` (change it with `DB_FILE`; `/data/aion2bot.db` in the Docker image). The driver is pure Go, so no C compiler is needed.
On first start, a `data/store.json` from older versions is imported and renamed to `store.json.imported`; servers from it are linked into one network, because older versions shared posts with every server.

## Project layout

```
main.go             startup, .env, database, shutdown
game.json           dungeons, modes, roles and classes
Dockerfile          the release image; compose.yaml runs it
.github/workflows   CI (tests, image build) and releases
internal/config     game.json and .env loading, role/class matching
internal/lfg        post rules, SQLite storage, networks, profiles
internal/bot        commands, panels, forms, cross-server sync, pings, cleanup
```

Run the tests with `go test ./...`.

> **Note:** `go.mod` pins discordgo to a commit on `master`, because the Label component used for selects inside modals isn't in a tagged release yet (v0.29.0).
