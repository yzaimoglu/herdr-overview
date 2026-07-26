# Herdr overview

Local operations console for Herdr agents. The frontend is an Astro React island using Kumo UI components. The Go API is the only process that calls the `herdr` CLI; Caddy serves the built frontend and reverse proxies `/api/*` to it.

## Run locally

Install Bun and Go, then start the API and frontend in separate terminals:

```sh
cd server && go run .
bun install
bun dev
```

The frontend runs at `http://localhost:4321`. Set `PUBLIC_API_BASE=http://localhost:8787` when using the Astro dev server. In a built deployment, the API is same-origin through Caddy.

The API uses `HERDR_BIN_PATH` when set, otherwise it looks up `herdr` on `PATH`. It falls back to demo data when the command is unavailable. Set `HERDR_DEMO=false` to fail instead of showing demo data.

## Run with Docker

The compose setup exposes Caddy on port 80 and keeps the API private on the compose network:

```sh
docker compose up --build
```

The API service mounts the host Herdr binary and home directory read-only. Set `HERDR_HOST_BIN` if Herdr is installed somewhere other than `~/.local/bin/herdr`. Open `http://localhost` after the containers start. The Caddy port is also published on the host's NetBird `wt0` address; set `NETBIRD_IP` if that address changes.

Both services use Docker's `unless-stopped` restart policy, so they come back after Docker or host reboots. `docker compose down` removes the containers and is the intentional way to stop the overview.

## Discord setup

Create a Discord application in the [Discord Developer Portal](https://discord.com/developers/applications), add a bot, and copy its token into an untracked `.env` file. Never commit the token. In the application's **Bot** settings, enable the **Message Content Intent** under **Privileged Gateway Intents**.

Invite the bot to the target guild with the `bot` scope and these permissions on the target forum channel:

- View Channel
- Read Message History
- Send Messages
- Send Messages in Threads
- Create Public Threads
- Manage Threads

Create a forum channel for Herdr agents in the target guild. Copy the guild and forum channel IDs with Discord Developer Mode enabled. Add the Discord user IDs allowed to control agents as a comma-separated list. The adapter ignores messages from all other users.

Set these variables in the untracked `.env` file:

```dotenv
DISCORD_BOT_TOKEN=<bot-token>
DISCORD_GUILD_ID=<guild-id>
DISCORD_FORUM_CHANNEL_ID=<forum-channel-id>
DISCORD_ALLOWED_USER_IDS=<user-id>,<another-user-id>
```

`HERDR_OVERVIEW_API_URL` defaults to `http://overview-api:8787`, `DISCORD_SYNC_INTERVAL` defaults to `10s`, and `DISCORD_STATE_PATH` defaults to `/data/discord-state.json`. The `discord-state` named volume stores the thread mappings and synchronization state at `/data`, so normal `docker compose down` and container recreation preserve it. Use `docker compose down -v` only when intentionally discarding that state.

### Discord smoke test

1. Start Compose with the required Discord variables: `docker compose up --build`.
2. Confirm the bot connects without logging the token.
3. Confirm every current Herdr agent has one forum thread.
4. Send a normal message as an allowed user and verify the API receives a prompt.
5. Send `/interrupt` and verify the thread reports the action.
6. Close an agent through the web UI and verify the matching thread archives after reconciliation.

## API surface

- `GET /api/health` reports API and Herdr availability.
- `GET /api/overview` lists agents, summary counts, and recent control activity.
- `GET /api/agents/:pane/output` reads recent terminal output.
- `POST /api/agents/:pane/prompt` sends a guarded prompt to an agent.
- `POST /api/agents/:pane/interrupt` sends `ctrl+c`.
- `POST /api/agents/:pane/close` closes the pane.
- `POST /api/agents` starts an agent with `name`, `kind`, `pane`, and optional `args`.
