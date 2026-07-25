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

## API surface

- `GET /api/health` reports API and Herdr availability.
- `GET /api/overview` lists agents, summary counts, and recent control activity.
- `GET /api/agents/:pane/output` reads recent terminal output.
- `POST /api/agents/:pane/prompt` sends a guarded prompt to an agent.
- `POST /api/agents/:pane/interrupt` sends `ctrl+c`.
- `POST /api/agents/:pane/close` closes the pane.
- `POST /api/agents` starts an agent with `name`, `kind`, `pane`, and optional `args`.
