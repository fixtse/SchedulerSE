# SchedulerSE

A lightweight, self-hosted HTTP POST scheduler with an authenticated web interface.
Create tasks, choose their timezones, build schedules, and run tasks manually.
SQLite stores task settings, credentials, account information, and execution history.
Requests and Telegram alerts are attempted once, without automatic retries.

Licensed under the [MIT License](LICENSE).

## Getting started

Requires Docker Engine and Docker Compose v2.24+. Use a prebuilt image; GitHub Actions
owns verification and image publishing. The deployment server does not need Go or a
local build toolchain.

```sh
git clone https://github.com/fixtse/SchedulerSE.git
cd SchedulerSE
cp .env.example .env
chmod 600 .env
```

Set `SCHEDULERSE_IMAGE` to a published image tag or digest. The default image is
`ghcr.io/fixtse/schedulerse:latest`. If you fork the repository, use the image published
under your own owner/repository. Registry authentication is needed only for private images.
The scheduler is always active. New tasks start disabled so you can finish configuring
them before enabling their schedules. Configure optional Telegram notifications in the
web interface; no Telegram credentials are needed in `.env`.

```sh
docker compose pull
docker compose up -d --wait
```

Open **http://127.0.0.1:8789** on the server, or access it through an SSH tunnel:

```sh
ssh -L 8789:127.0.0.1:8789 your-server
```

On first visit, create an administrator account using the one-time setup token:

```sh
docker compose exec schedulerse /app/schedulerse setup-token
```

Usernames accept 3–64 letters, digits, dots, underscores or hyphens. Passwords must be
12–72 bytes. Setup closes once the administrator account exists.

**Fresh installations start with no tasks.** Add tasks in the web interface, including
the endpoint URL, optional authorization header, and JSON body. Authorization values
include the complete header value, such as `Bearer …`; saved headers are never displayed.
New tasks start with scheduled runs disabled. Payload values retain their JSON types.

Each task has an optional **Request timeout (milliseconds)** in the editor. Set
100–600000 ms (up to 10 minutes), or leave it blank to inherit `REQUEST_TIMEOUT_MS`
from the server (default: 120000 ms). The saved timeout applies to scheduled and
manual requests, including response-body reads. Existing tasks inherit the server
default until an override is saved. Telegram delivery keeps its separate timeout.

## Scheduling and manual runs

Choose **Manual only**, **Every few minutes**, **Hourly**, **Daily**, **Weekly**, or
**Monthly**, then choose a timezone. Weekly schedules support multiple weekdays.
Monthly schedules skip months without the selected day. **Advanced cron** supports
five fields, or six including seconds.

Times follow the task's timezone, including daylight-saving changes: skipped local
times do not run, and repeated local times can run twice. Next runs display in each
task's timezone; execution history uses UTC. The server timezone, `TZ`, is the fallback
for tasks without an explicit timezone; its default remains `Europe/Madrid` for upgrade
compatibility.

Enable or disable scheduled execution for each task in the web interface. The scheduler
runs whenever the service is running; there is no global off switch. Old environment
settings cannot disable it. Run only one scheduler for a given set of endpoints.

Use **Run now** on any task card or editor to execute its saved configuration once,
even when scheduled runs for that task are disabled. Save edits first.
Runs continue in the background; refresh recent activity to see the result. Duplicate
form submissions and overlapping runs of the same task are rejected. Running a task
manually does not enable its schedule.

## Telegram notifications

Open **Telegram settings** after signing in. Enter the bot token from @BotFather and
the destination chat ID (or public channel @username), enable notifications, and save.
Start a conversation with the bot or give it permission to send messages in your group
or channel. Tokens are never displayed after saving; leave the token field blank to
keep the saved value. You can replace or remove it, and disable notifications in the UI.

Choose **Failures and timeouts**, **Successful completions**, and/or **Fallback warnings**
in **Notification types**. Choices apply immediately to both scheduled and manual runs.
Fallback warnings are separate from successful completions, so a fallback produces at
most one message. Existing settings retain failure and fallback alerts; success messages
start disabled. You can leave all types unchecked to silence task events while keeping
the connection enabled. Interrupted runs during shutdown remain silent.

Click **Test Telegram** to send one message with the saved credentials, regardless of
the selected notification types. Save edits
before testing, check the selected chat, and wait 10 seconds between tests. Tests are
recorded before sending to prevent duplicate submissions and concurrent sends. Errors
never expose the token or Telegram response, and uncertain sends are never retried
automatically. Notification changes apply immediately without restarting the service.

Existing environment credentials are imported into SQLite once during the first upgrade.
After import you can remove `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID` from `.env`;
subsequent changes, including disabling notifications, belong to the UI and will not be
overwritten by old environment values. Keep the state volume to retain the connection.

## Upgrading and persistent storage

```sh
docker compose pull
docker compose up -d --wait
docker compose logs --tail 50 schedulerse
```

Set `SCHEDULERSE_IMAGE` to a published `sha-<commit>` tag or digest to pin a release.
For automatic deployment with Dokploy, configure the repository Actions secret
`DOKPLOY_SCHEDULERSE_DEPLOY_HOOK` with the service's deploy URL, and configure the
service to pull the prebuilt `latest` image. Successful pushes to `main` deploy only
after verification, image publishing, and the isolated image smoke test pass.
Manual runs of the publishing pipeline on `main` also deploy. Pull requests and
version-tag builds do not deploy. The hook is called once, without automatic retries,
and its URL is never printed in logs. Without the secret, deployment is skipped;
verification and publishing still work for forks and standalone installations.

**Existing saved tasks survive this update.** Startup does not seed, overwrite, or delete
tasks. Keep the same `.env`, `TZ`, and state volume to retain schedules, timezones,
authorization headers, payloads, enabled settings, account, and execution history.
Compatibility handling retains the validation, alert behavior, credential requirements,
and manual-only restrictions of tasks saved by older releases. These tasks are not
bundled into new installations. Old service-specific job environment variables are no
longer read; edit existing tasks in the web interface.

`SCHEDULERSE_STATE_VOLUME` selects the persistent Docker volume; its default remains
`schedulerse_state`. Keep this value when redeploying, even if you rename or move the
checkout. Stop the service before backing up the volume, and store `.env` separately.
`docker compose down` retains data; `docker compose down -v` deletes it.

## Access and configuration

`MANAGEMENT_BIND` and `MANAGEMENT_PORT` are optional Docker Compose settings. They
select the host interface and port used to publish the management UI. Omitting them
uses `127.0.0.1` and `8789`, so the UI is accessible from the host or an SSH tunnel.
For example, `MANAGEMENT_PORT=9000` publishes the UI at `http://127.0.0.1:9000` while
the container continues listening on port `8789`.

`LISTEN_ADDR` controls where the application itself listens. `:8789` means port `8789`
on all interfaces inside the container, allowing Docker's port mapping and reverse
proxies on the container network to reach it. The Dockerfile defaults to `:8789`, and
Compose explicitly sets that value to match its container port mapping. Setting
`LISTEN_ADDR` in `.env` does not override Compose's explicit value. Use the
`MANAGEMENT_*` settings to change host publishing; changing the internal listener
requires updating Compose's `environment` and container port mapping together.
When running the binary directly, `LISTEN_ADDR` can select an address such as
`127.0.0.1:8789`; if unset, it defaults to `:8789` (or the legacy `HEALTH_PORT` value).
The healthcheck command uses the same listener port.

For HTTPS through a reverse proxy, set `COOKIE_SECURE=true`, preserve the original
`Host` header, and exclude `/health` from proxying. Setup and login are rate limited;
changes require an authenticated session and a CSRF token.

Keep `.env`, saved state, credentials, and setup tokens out of source control and images.
The runtime image contains the application, required certificates, and its license.

## CI and publishing

GitHub Actions checks formatting, vet, race tests, and Compose configuration using
`.env.example`, then builds the AMD64 runtime image and smoke tests it in an isolated
container without network access. Pushes to `main` and version tags publish to GHCR
under the repository's owner and name; pull requests build and verify without publishing.
Forks need GitHub Actions enabled and permission to publish packages. Configure package
visibility in GHCR if you want anonymous pulls; CI does not change repository or package
visibility.

Go 1.27+ and the pinned Docker toolchain are required. Module and Docker dependency
caches avoid repeated SQLite C compilation. Local builds, tests, and container smoke
tests must not run on the resource-constrained deployment server; use Actions for verification.
