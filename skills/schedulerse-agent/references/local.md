# Local agent, including a Docker scheduler

This is the default. The agent bridge makes outbound authenticated requests to
SchedulerSE, stores each due event locally, and claims pickup before invoking the
agent. No webhook receiver, callback URL, tunnel, public hosting or Cloudflare
account is required. The initializer uses the API token to configure polling
automatically; the user does not need to select a delivery method in settings.

## Address the service from where the bridge runs

- Bridge in a container on the same Compose network: use the Docker service name
  and internal port, for example `http://schedulerse:8789` with this repository's
  Compose service. Docker DNS resolves that name only on the shared network.
- Bridge on the host: use SchedulerSE's existing published URL/host port from that
  host. A Docker service name is not usually resolvable from the host.
- Separate Compose projects must join a shared user-defined network to use service
  DNS. Do not change unrelated networks or expose the management port publicly.

The bridge opens no listening port and does not need `host.docker.internal` or
host networking. Do not configure a container callback to the host's loopback.
Use HTTP only on the user's trusted local/Docker network; use HTTPS otherwise.
The scheduler container never needs to connect back to the agent container/host.

## Initialize once

Python 3 and the chosen agent's executable must be installed **where the bridge
runs**. A Python-only container cannot wake a Codex executable that exists only on
the host. Prefer running the bridge beside the user's existing Codex installation;
if containerizing the bridge, provide that container's agent runtime, authentication,
workspace and persistent state directory explicitly. Do not install an agent runtime
inside the SchedulerSE container.

Store the SchedulerSE API token in a private file using the available secret store
or private credential UI. Never send it in chat or include it in command arguments.
Read `connection.json` from the installed skill for the service URL. The download
captures the browser address automatically, including HTTP/HTTPS and any port.
The initializer uses it by default and calls PUT `/connection` to select polling.
The agent chooses the paths and routing label from its own runtime/session:


```sh
python3 /installed/skill/scripts/bridge.py init-local \
  --state /private/schedulerse-connection \
  --token-file /private/scheduler-api-token \
  --workdir /agent/workspace \
  --conversation-id reminders-for-project
```

Only use `--scheduler-url` to override a packaged address that is unreachable from
the agent's namespace, for example a known `http://schedulerse:8789` on its shared
Docker network. This is an agent setup detail, not another required user input.
A source-only skill copy without `connection.json` needs an explicit URL once.
Omitting `--token-file` prompts
for the token without echo when running interactively. The helper creates private
configuration with a stable random `consumer_id`. Preserve it with the SQLite
receipts across restarts; it resolves lost pickup responses without duplicating work.
Do not copy this consumer identity/state to a second simultaneously running bridge.

Default adapter: a non-interactive, read-only `codex exec` turn, with its answer in
a private `.result.txt` file beside the receipts. Add `--codex-session <explicit-id>`
to resume an existing Codex CLI session. Never use `--last`, a T3 thread ID, or an
agent-supplied executable/permission flag from the event payload. CLI output is not
a T3 thread message or push notification. Use a native T3 integration when that
is the requested destination. For another agent, configure trusted local argv using
`--command-json '["/absolute/path/to/agent-adapter"]'`; it receives JSON on stdin.

Start the consumer:

```sh
python3 /installed/skill/scripts/bridge.py run --state /private/schedulerse-connection
```

Keep it running with the user's process supervisor or container restart policy.
Persist the state directory on the bridge's host/volume. Use the user's agent runtime
and auth configuration, not a new unattended login or expanded permissions. An agent
shell that ends after this turn is not a persistent installation.

Schedule one setup check with `context.conversation_id` equal to the configured
routing label. Verify pickup and actual agent output. Local polling checks every
five seconds when idle; an agent run can delay subsequent polling. No AI process
or request is started for an empty inbox. All agent containers need their own
normal inference connectivity/authentication; polling does not supply free inference.

## Delivery behavior

The inbox only returns due pending occurrences for the configured conversation.
The bridge saves a durable `awaiting` receipt before sending a pickup acknowledgement.
SchedulerSE atomically assigns each occurrence to one random consumer identity.
A retry with the same identity succeeds; another consumer receives 409 and skips
execution. A local receipt becomes executable only after successful acknowledgement.
If the response is lost, the receipt remains awaiting and retries even when the task
no longer appears in the inbox. Cancellation or a conflicting consumer also returns
409 and skips the local invocation. These APIs require the same scoped agent token;
the token also configures delivery through GET/PUT `/connection`. Account and
unrelated application settings still require the authenticated browser UI.

Pending events wait while the bridge is stopped. A successful pickup marks a one-shot
item delivered or advances a recurring item, skipping missed slots. This records
acceptance, not completion by the AI. A local failure does not roll back a pickup
or stop future recurring pickups. Failed/interrupted agent invocations remain in the
local receipts and are not automatically replayed. Inspect them before explicitly
repeating an action. On restart, a `running` local invocation becomes `interrupted`.

Existing webhook tasks need a matching `context.conversation_id` to be picked up;
items without one remain pending and should be cancelled/recreated with the intended
routing label. Changing from webhook to polling affects pending occurrences only. An in-flight
webhook may still complete. Revoking the API token stops pickup; existing pending
schedules are retained. Cancellation does not undo an occurrence already picked up.
