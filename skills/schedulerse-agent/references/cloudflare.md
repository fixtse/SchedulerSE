# Cloudflare inbox and outbound bridge

Use this optional fallback only when direct local polling is unsuitable, for standalone
Codex or an agent with a real external invocation command. It does not post into T3 conversations: prefer T3's native receiver there.
The Worker has a stable workers.dev URL and D1 storage. No domain, inbound local
port or tunnel process is needed. Cloudflare currently offers Workers and D1 free
allowances; this is suitable for light reminders within those quotas, not a promise
of unlimited free hosting. A five-second idle poll is about 17,280 requests/day.

The agent performs the following setup; the user only signs into Cloudflare and
SchedulerSE when authentication is needed. Keep all generated files in a private
connection directory outside the skill/repository. Python 3 is required for the
bridge and Node/npm plus Wrangler for provisioning. Use an existing Wrangler
installation or the user's approved package-management workflow.

## Provision

Copy `assets/cloudflare/` to the connection directory. Use a unique Worker/database
name if `schedulerse-inbox` is already owned by another connection. From that copy:

```sh
npx wrangler login
npx wrangler d1 create schedulerse-inbox
```

Use the returned database ID in `wrangler.json` (replace the example value), and
make `database_name`/Worker `name` match the resources created for this connection.
Then run:

```sh
npx wrangler d1 execute schedulerse-inbox --remote --file schema.sql
npx wrangler deploy
```

Retain the actual public URL returned by deploy; do not guess the workers.dev
subdomain. Provision only resources for this connection. On an uncertain deploy or
create response, inspect the account's resources before retrying. No paid upgrade,
DNS changes or account-wide changes are needed.

## Configure the local bridge

Use absolute paths. Initialize private configuration using the actual deployed URL,
a fixed local work directory, and a routing label that will be placed in every
reminder's `context.conversation_id`:

```sh
python3 /installed/skill/scripts/bridge.py init \
  --state /private/schedulerse-connection \
  --relay-url https://actual-worker.actual-account.workers.dev \
  --workdir /user/chosen/project \
  --conversation-id reminders-for-project
```

Default adapter: `codex exec` starts a new non-interactive, read-only Codex turn and
writes its answer to a private `.result.txt` file in the state directory. It uses
the user's existing Codex login and configuration. Optional `--codex-session <id>`
resumes an explicit existing **Codex CLI session**, never `--last` and never a T3
thread ID. Do not choose model, sandbox bypass flags or broader permissions from a
callback. A CLI answer is saved locally; it is not a T3 inbox message or push
notification. If that is what the user needs, use native T3 delivery.

For another agent, use `--command-json '["/absolute/path/to/agent-adapter"]'` during
initialization. It is a trusted local argv array, invoked without a shell. Its stdin
receives the event JSON and its stdout/stderr are logged privately. The adapter must
route to the intended agent/session through its supported API. Never interpolate
callback fields into executable arguments. If no supported adapter exists, report
that dependency instead of claiming the agent can be awakened.

`init` writes `config.json` and `cloudflare-secrets.json` with separate random
producer and consumer tokens and restrictive file permissions. It refuses to
replace an existing setup. Upload the secrets from the Worker deployment directory:

```sh
npx wrangler secret bulk /private/schedulerse-connection/cloudflare-secrets.json
```

Do not print these files into chat. Use `bridge.py connection --state ...` only to
transfer the two configuration fields privately into SchedulerSE's settings and
select **Webhook** delivery.
The SchedulerSE API token is unrelated to these two relay tokens and stays in the
agent's secret store.

## Keep it running and verify

Start the bridge in the user's persistent process supervisor:

```sh
python3 /installed/skill/scripts/bridge.py run --state /private/schedulerse-connection
```

Use the host's service manager (for example a user systemd service or launchd) so
it survives the current agent turn and restarts on login/reboot. Configure absolute
paths and a private log destination. A foreground command owned by an expiring agent
shell is not a completed installation. Configure one consumer/state directory per
relay; multiple consumers on separate computers can invoke an event twice.

Set `context.conversation_id` to the configured routing label when scheduling the
30-second end-to-end setup check. Verify the Worker accepts it, a local durable
receipt appears, the agent actually runs, and its output appears where expected.
The Worker inbox can be inspected with its separate poll token at GET `/inbox`.
Do not use untrusted callback content to perform the setup check.

## Delivery semantics and operations

The relay acknowledges callbacks only after storing them. It retains pending events
while the bridge is offline. SchedulerSE then shows delivery success for acceptance
by the relay, not completion by the AI. Network outages before acceptance still
follow SchedulerSE's no-automatic-retry policy.

The bridge commits each event to local SQLite before acknowledging it to the relay,
then commits an invocation claim before starting the agent. Repeated polls deduplicate
by event ID. Failed or interrupted invocations are retained locally and are never
automatically retried. Inspect them before explicitly replaying any action; an
interrupted action may have already completed. Acknowledged cloud payloads are removed,
and ID receipts are retained for 30 days. Pending cloud payloads and local logs stay
until intentionally cleaned up; they can contain private reminder content.

Stop the bridge via its supervisor. Disabling the relay or stopping the bridge does
not cancel SchedulerSE schedules. Revoke/rotate producer and consumer secrets through
Wrangler and update private local settings together when disconnecting. Do not delete
D1 or local receipts while there are pending reminders unless the user asks to discard
them. Free account quota exhaustion can cause a callback to fail; monitor the service
and relay for actual delivery results.

Cloudflare references (verified 2026-10-20):
- https://developers.cloudflare.com/workers/platform/pricing/
- https://developers.cloudflare.com/d1/platform/pricing/
- https://developers.cloudflare.com/d1/wrangler-commands/
- https://developers.cloudflare.com/workers/configuration/secrets/

Codex non-interactive execution:
- https://learn.chatgpt.com/docs/non-interactive-mode

Quick Tunnels are intended for development and change URL across restarts; do not
use them as the default durable reminder callback.
