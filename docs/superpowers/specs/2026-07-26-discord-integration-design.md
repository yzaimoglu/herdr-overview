# Discord Integration Design

Status: Approved for planning

## Summary

Add a separate Go Discord adapter service to Herdr Overview. The adapter mirrors each Herdr agent into a thread in one configured Discord forum channel and lets configured Discord users control agents through those threads. The web dashboard remains the primary UI and continues working if Discord is unavailable.

## Goals

- Create one Discord forum thread for every discovered Herdr agent.
- Automatically create threads for existing and newly discovered agents.
- Archive a thread when its agent is closed or no longer appears in a successful Herdr agent listing.
- Forward every normal message from an authorized Discord user in an agent thread as a Herdr prompt.
- Support `/interrupt` and `/close` control messages.
- Post lifecycle changes and relevant new agent output into the matching thread.
- Persist the pane-to-thread mapping across bot restarts.
- Keep the Discord bot isolated from the web/API process.

## Non-Goals

- Starting agents from Discord.
- Replacing the web dashboard.
- A Herdr event-stream implementation.
- Discord slash-command registration for prompts or controls.
- Full transcript storage or replay.

## Architecture

Add a `discord/` Go service and a `discord-bot` Docker Compose service.

The bot connects to Discord through the Gateway and calls the existing API over the private Docker network at `http://overview-api:8787`. It does not invoke the Herdr CLI directly. Existing API routes are sufficient for the first version:

- `GET /api/overview`
- `GET /api/agents/:pane/output`
- `POST /api/agents/:pane/prompt`
- `POST /api/agents/:pane/interrupt`
- `POST /api/agents/:pane/close`

The bot uses a maintained Go Discord Gateway client, `discordgo`, for Gateway events and Discord REST operations.

## Configuration

All configuration is runtime-only. Secrets must not be committed.

- `DISCORD_BOT_TOKEN`: Discord bot token.
- `DISCORD_GUILD_ID`: allowed Discord server ID.
- `DISCORD_FORUM_CHANNEL_ID`: forum channel where agent threads are created.
- `DISCORD_ALLOWED_USER_IDS`: comma-separated Discord user IDs allowed to control agents.
- `HERDR_OVERVIEW_API_URL`: internal API URL, defaulting to `http://overview-api:8787`.
- `DISCORD_STATE_PATH`: state file path, defaulting to `/data/discord-state.json`.
- `DISCORD_SYNC_INTERVAL`: reconciliation interval, defaulting to 10 seconds.

The bot requires permission to view the forum, create public threads, send messages in threads, read message history, and manage threads for archiving.

## Thread Lifecycle

Each thread is identified by the Herdr `paneId`, not by the agent name or session ID. Thread names use the agent name when available and always include the pane ID so mappings can be recovered.

On each successful overview sync:

1. List the current agents.
2. For each agent, load its persisted mapping.
3. If no mapping exists, find an existing thread containing the pane ID before creating a new thread.
4. If no matching thread exists, create a forum thread and post an initial status message.
5. Update the recorded status and output fingerprint.
6. For mapped agents no longer present, post a closure/unavailable notice and archive the thread.

A failed overview request does not archive or alter agent mappings. The bot retries on the next sync interval.

If a mapped Discord thread was manually deleted, the bot creates a replacement thread and records the new ID.

## Discord Inbound Messages

The bot accepts messages only when all of these are true:

- The message is from the configured guild.
- The message belongs to a thread under the configured forum channel.
- The author ID is in `DISCORD_ALLOWED_USER_IDS`.
- The author is not the bot itself.

Normal messages are forwarded unchanged as prompts to the mapped Herdr pane. Discord messages are limited to Discord's normal message size; no additional prompt splitting is needed in the first version.

Control messages are exact commands:

- `/interrupt`: call the existing interrupt endpoint and report success or failure.
- `/close`: call the existing close endpoint, report success or failure, then archive the thread after the next successful reconciliation.

Unauthorized messages do not reach Herdr. The bot may post a short permission notice, but it must not reveal configuration or API details.

## Discord Outbound Messages

Each newly created thread receives an initial message containing:

- Agent name and provider.
- Pane ID and workspace.
- Current status.
- A short explanation that normal messages are prompts and `/interrupt` and `/close` are controls.

The bot posts lifecycle updates when an agent changes status, becomes unavailable, is closed, or is recreated after a deleted thread.

After an authorized prompt, the bot polls the agent output and posts the resulting changed output once it has a meaningful update. Output observed from outside Discord may also be posted when it represents a meaningful change. Unchanged output is never reposted.

Output messages are formatted as plain text or fenced code and chunked below Discord's message limit. The bot coalesces output within one sync interval to avoid posting one message per terminal fragment.

## State Persistence

The bot stores state in an atomic JSON file mounted from a dedicated Docker volume.

Each record contains:

- Herdr pane ID.
- Discord thread ID.
- Last known agent status.
- Last output fingerprint.
- Last synchronization timestamp.
- Closed/archive state.

Writes go to a temporary file in the same directory, followed by an atomic rename. The state volume is not copied into the image and is ignored by Git/Docker source contexts.

If the state file is absent, the bot searches active and archived forum threads for the pane ID before creating new threads. This prevents duplicates after state recovery when Discord still has the original threads.

## Failure Handling

- Discord Gateway disconnects use the library's reconnect behavior with bounded retry logging.
- Discord REST failures leave the mapping intact and retry on the next sync.
- API or Herdr failures are reported in the affected thread without archiving it.
- Discord downtime does not affect the web API or dashboard.
- A malformed state file is moved aside with a warning and rebuilt through thread reconciliation rather than silently overwriting it.
- Duplicate syncs are safe because thread identity is keyed by pane ID and state is persisted after each successful mapping change.

## Testing

The Discord service must have a fake Discord client and fake overview API so synchronization logic does not require live credentials.

Required tests:

- Create a thread for a new agent.
- Resync an existing agent without creating a duplicate.
- Recover a mapping by searching a thread name after state loss.
- Archive a thread after a successful agent listing omits the pane.
- Do not archive agents when the overview API fails.
- Forward an authorized normal message as a prompt.
- Reject unauthorized and bot-authored messages.
- Execute `/interrupt` and `/close` controls.
- Ignore unrelated guilds, channels, and non-thread messages.
- Deduplicate unchanged output and chunk long output.
- Persist and reload state atomically.
- Recreate a mapping when a Discord thread is deleted.

The existing frontend build and Go API tests remain required.

## Deployment

Add the bot service to `docker-compose.yml` with a dedicated data volume, runtime environment configuration, private API dependency, and `unless-stopped` restart policy. Add a bot Dockerfile or build target that compiles only the Discord service.

Update `README.md` with Discord application setup, required bot permissions, forum-channel configuration, allowed user IDs, environment variables, state-volume behavior, and a first-run smoke test.

## Deferred Work

- Replace polling with a Herdr event stream.
- Start-agent commands from Discord.
- Rich Discord buttons, modals, or slash commands.
- Full output transcript persistence.
- Multi-server or multi-forum routing.
