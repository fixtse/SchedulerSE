# Bash scripts: setup and technical details

[Back to the README](../README.md#bash-scripts)

## Check first

From the project folder, run:

```sh
sh scripts/check-bash.sh
```

This starts a temporary container using your `compose.yaml`, runs the application's
own requirements checks, then removes the container. It does not enable Bash,
change the running service, read the database, or install packages. Docker may
pull the configured image, so use an updated image containing the checker.

You can also run the command directly:

```sh
docker compose run --rm --no-deps -T --entrypoint /app/schedulerse schedulerse check-bash
```

The result lists Linux support, required tools, resource limits and a real sandbox
test. **All checks must pass**; exit code 0 means ready, and 1 means something needs
attention. Checks still work when Bash is disabled or the normal service cannot
start. If Docker is missing, follow the [Docker installation guide](https://docs.docker.com/engine/install/)
and [Compose instructions](https://docs.docker.com/compose/install/linux/).

For a native installation, run the binary instead (replace its path as needed):

```sh
sh scripts/check-bash.sh --native /path/to/schedulerse
# Or: /path/to/schedulerse check-bash
```

A host check does not tell you whether a container is ready, or vice versa. Run the
check with the same user and service restrictions you intend to use for scripts.

## Fix a failed check

### Missing tools

The SchedulerSE image already includes the tools. For Docker installations, update
it and rerun the check:

```sh
docker compose pull
sh scripts/check-bash.sh
```

For a **native Ubuntu/Debian installation**, install them on the host:

```sh
sudo apt-get update
sudo apt-get install -y bash bubblewrap util-linux ca-certificates curl
```

On other Linux distributions, install `bash`, `bubblewrap` and `util-linux` with
your package manager.

Installing host packages does not change the tools or permissions inside an
existing Docker container.

### Missing resource limits

For Docker, keep these settings on the `schedulerse` service in `compose.yaml`
(the supplied file already has them), then rerun the checker:

```yaml
mem_limit: 512m
memswap_limit: 512m
cpus: 1.0
pids_limit: 128
```

The checker uses the updated settings in its temporary container. Apply them to
the actual service with `docker compose up -d` when you are ready to enable Bash.

For a native Linux installation using systemd, you can test with temporary limits:

```sh
systemd-run --user --scope \
  -p MemoryMax=512M -p MemorySwapMax=0 \
  -p TasksMax=128 -p CPUQuota=100% \
  /absolute/path/to/schedulerse check-bash
```

These limits apply to that check only. To keep them for an existing system service
named `schedulerse`, run `sudo systemctl edit schedulerse` and add:

```ini
[Service]
MemoryMax=512M
MemorySwapMax=0
TasksMax=128
CPUQuota=100%
```

Save, then run `sudo systemctl daemon-reload`. Restart the service when ready to
apply its new limits. Use the actual unit name if yours is different, or
`systemctl --user edit` / `systemctl --user daemon-reload` for a user service.
An older host without cgroup v2 support needs a supported Linux setup; installing
Bubblewrap alone cannot provide these limits. See [systemd's resource settings](https://www.freedesktop.org/software/systemd/man/latest/systemd.resource-control.html).

### Sandbox blocked on Ubuntu

For native Ubuntu 24.04 installations, AppArmor may require the distro's
Bubblewrap policy. Install and load it without turning off AppArmor:

```sh
sudo apt-get install -y apparmor-profiles
if [ ! -f /etc/apparmor.d/bwrap-userns-restrict ]; then
  sudo install -m 0644 \
    /usr/share/apparmor/extra-profiles/bwrap-userns-restrict \
    /etc/apparmor.d/bwrap-userns-restrict
fi
sudo apparmor_parser --replace /etc/apparmor.d/bwrap-userns-restrict
```

Then rerun the check. This changes the host's policy for Bubblewrap, so it should be
done by the machine's administrator. See [Ubuntu's AppArmor documentation](https://documentation.ubuntu.com/security/security-features/privilege-restriction/apparmor/).

### Sandbox still blocked in Docker

Some Docker hosts block the extra isolation Bash needs. A package installation
or a host-only check cannot override that container policy. Keep Bash disabled
and use HTTP POST tasks, or use a supported native Linux setup. Do not enable
privileged mode, disable the container's security profiles, or mount the Docker
socket just to make the check pass.

## Execution details

Bash is **disabled by default**, including existing saved scripts. HTTP automations
and reminders continue to work. Use HTTP POST tasks when your work needs a network
request.

Administrators may set `ENABLE_BASH=true` to enable isolated scripts, then separately
set `ENABLE_AGENT_BASH=true` to allow agents to create or run them. Both flags are
strict booleans and require a restart. In Compose, edit the explicit values under
`environment`; an `.env` value does not override those entries.

Enabling Bash requires Linux, `bubblewrap`, `prlimit`, working unprivileged user/PID/
mount namespaces, and cgroup v2 limits of at most 512 MiB memory, 128 PIDs, and one
CPU core. These tools are included in the image. The service checks the resource
limits and runs a sandbox probe at startup; unsupported isolation fails closed.
Standard Docker security policies may block nested namespaces. Keep Bash disabled
on those hosts; do not add privileged mode, host mounts, the Docker socket, or broad
capabilities to bypass the check. Native services need equivalent external cgroup
limits, for example systemd `MemoryMax=512M`, `MemorySwapMax=0`, `TasksMax=128`, and `CPUQuota=100%`.

Scripts run with `bash -e -o pipefail` in fresh namespaces with an empty environment
apart from `PATH` and `HOME`. Scripts share the service network, including access
to DNS and HTTPS. Only
resolver settings and public certificate bundles are made available read-only;
application settings and private keys stay hidden. Scripts cannot access scheduler
data, home directories, saved credentials, or persistent output. System commands
and libraries are
mounted read-only. `/tmp` is private and limited to 16 MiB; every file is discarded
when the run ends. Existing scripts that use `/data` or inherited environment values
must be updated.

```bash
printf '%s\n' '{"message":"Hello from SchedulerSE"}' > /tmp/message.json
jq -e '.message | length > 0' /tmp/message.json > /dev/null
```

Each run has its own PID namespace with Bubblewrap retained as its PID 1 supervisor.
Completion or timeout destroys its descendants,
including detached processes. Process limits cap address space at 256 MiB, CPU time
at 30 seconds per process, file size at 16 MiB, open files at 128, and processes at
64 per sandbox user namespace. The execution timeout is also enforced, initially
120 seconds and at most 600 seconds. Output is discarded; exit zero records success,
a nonzero exit records `SCRIPT_FAILED`, and timeout records `SCRIPT_ABORTED`.
HTTP retries and fallback are disabled for scripts.

The scheduler admits at most four simultaneous executions. When capacity is full,
manual requests are rejected and scheduled occurrences are skipped rather than
queued or replayed. Compose also limits the
whole service to 512 MiB memory, one CPU, 128 PIDs and rotating logs. Scripts share
this aggregate budget with the scheduler, so resource abuse can still interrupt
service availability; this is not a multi-tenant compute service. Keep enabled
agent clients trusted and give them only the tools they need.

Storage is bounded: 1,000 automation jobs, 1,000 reminder records, 100,000 execution
records and 100,000 pickup receipts. SQLite growth is capped at 32,768 pages
(normally 128 MiB). Capacity exhaustion rejects new work. Records are not silently
deleted because that would weaken idempotency; monitor capacity and archive state
during administrator-controlled maintenance.

