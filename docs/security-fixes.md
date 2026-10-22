# Security fixes following the 22 October audit

The six findings in `security-audit-2026-10-22.md` are addressed as follows:

- SEC-01: Bash and agent Bash have independent default-off administrator switches.
  The native shell runner has been replaced with a fail-closed bubblewrap sandbox
  exposing only read-only commands/libraries, a private `/tmp`, private processes,
  no inherited credentials, and explicitly enabled service network access. Existing saved Bash cannot bypass the
  execution adapter's disable switch.
- SEC-02: Bubblewrap retains its trusted PID 1 supervisor in each PID namespace,
  so both successful exit and cancellation destroy detached descendants. No process-group-only fallback
  remains. Further user namespaces are disabled inside the sandbox.
- SEC-03: four simultaneous executions, bounded record counts and database growth,
  per-process limits, bounded temporary storage, and Compose CPU/memory/PID/log
  limits constrain resource use. Bash startup requires externally enforced cgroup
  v2 CPU/memory/PID budgets. Service availability still shares the aggregate
  resource budget; dedicated per-run memory cgroups and multi-tenant isolation
  are not implemented.
- SEC-04: credential-bearing webhook, HTTP, and fallback destinations cannot change
  origin while retaining the same saved secret. Storage validates this atomically
  with mutations, and browser/API handlers explain the required clear/replacement.
- SEC-05: remote bridge HTTP is rejected at setup and every request unless an
  explicit insecure-network opt-in is configured. Loopback HTTP still works.
- SEC-06: bounded, expiring per-source login/reset budgets replace the global
  five-attempt budget. Trusted proxy CIDRs support per-client budgets without
  accepting spoofed forwarding headers. Password hashing remains concurrency-limited.

See the README for migration steps and resource ceilings. Upgrading disables all
Bash by default and removes inherited environment secrets and `/data` access even
when enabled. Network access is enabled for script automations. No live deployment or credentials were changed.

Security regression tests cover disabled execution, REST and MCP policy, host file
and environment isolation, HTTP/HTTPS connectivity and resolver/certificate availability, detached-child cleanup,
file budgets, aggregate-budget validation, admission limits and capacity release,
storage quotas, credential-origin binding, proxy header trust, and independent login
sources. CI requires the real Linux namespace tests to run rather than silently skip.
The namespace tests may skip on unsupported developer hosts unless
`REQUIRE_SCRIPT_SANDBOX=true` is set. Production enablement always probes isolation.

## Verification

- `REQUIRE_SCRIPT_SANDBOX=true go test -race -timeout 60s ./...`: passed, with real
  namespace tests required on this Linux host.
- Go vet, formatting, Compose validation and JavaScript syntax checks: passed.
- Python bridge suite: 12 tests passed.
- Pinned govulncheck v1.9.0: no reachable Go vulnerabilities. The existing
  module-only unused OpenPGP advisory remains outside the imported code.
- Alpine container race suite: passed with the container-specific 180-second
  timeout. Fixture seeding uses bulk inserts while checking the last permitted
  slot and first rejected slot through the real storage API.
- Runtime image: built successfully with bubblewrap and prlimit installed,
  running as UID/GID 1000:1000. Its network-isolated smoke test passed health,
  initial state, setup-token persistence, volume locking and restart checks under
  the configured CPU/memory/PID limits. The temporary smoke container and volume
  were removed by the test.

No production deployment, live credentials or existing agent connections were changed.

## Ubuntu 24.04 CI policy

Ubuntu 24.04 restricts capabilities in unprivileged user namespaces unless the
sandbox helper has an appropriate AppArmor profile. Installing Bubblewrap alone
is insufficient on runners where that profile has not been enabled. CI now
installs `apparmor-profiles` and loads the distro `bwrap-userns-restrict` policy
when the Ubuntu restriction is enabled. This retains the host-wide restriction
and the distro policy's restrictions on sandbox children; it does not disable
AppArmor or run the tests as root. `REQUIRE_SCRIPT_SANDBOX=true` remains mandatory.

The fixed startup probe now includes up to 4 KiB of Bubblewrap stderr on failure,
so policy denials and unsupported flags are visible. Only the fixed `true` probe
captures diagnostics; user-authored script output is still discarded. Native
regression checks cannot reproduce the hosted runner's AppArmor kernel policy;
the updated workflow must be rerun there to verify that policy installation.

References: [Ubuntu's namespace restrictions](https://documentation.ubuntu.com/release-notes/24.04/#unprivileged-user-namespace-restrictions)
and the [upstream distro policy](https://gitlab.com/apparmor/apparmor/-/blob/v4.0.1/profiles/apparmor/profiles/extras/bwrap-userns-restrict).

## Cancellation across AppArmor exec transitions

The runner no longer uses Bubblewrap's `--as-pid-1` option. With that option the
script replaced namespace init, so clearing its parent-death signal could let the
namespace survive cancellation of the outer launcher. AppArmor exec transitions
can clear that signal, and a script can explicitly clear it with `setpriv` too.

Bubblewrap now retains its built-in PID 1 supervisor. Its lifecycle remains tied
to the launcher, independent of the script's exec transitions and signal state.
When the main script exits, or cancellation kills the launcher, the supervisor
exits and the kernel destroys the namespace with all detached descendants.
The original exit/timeout regression test remains intact; a new regression clears
the script's parent-death signal explicitly. It reproduced the leak before this
change and passes with the supervisor retained.

## Patched Go toolchain

CI and the container builder use Go 1.27.3. The module also requires Go 1.27.3
so local builds cannot silently use an older vulnerable standard library.
This addresses [GO-2026-6809](https://pkg.go.dev/vuln/GO-2026-6809), whose fix is in
Go 1.27.3. Keep the vulnerability scan enabled; dependency changes alone cannot
patch the standard library linked into the application. Existing images must be
rebuilt with the patched builder to receive this fix.

Verified with the actual Go 1.27.3 toolchain: govulncheck v1.9.0 reports no reachable
vulnerabilities, and `make format-check check test compose-check` passes with
`REQUIRE_SCRIPT_SANDBOX=true`. The `golang:1.27.3-alpine` image manifest is available.

## Bash networking

Bash scripts intentionally share the service's network namespace, as requested for
network automations. Bubblewrap uses `--share-net` alongside its other isolation
options. Resolver settings, host-name mappings, and public CA bundles are exposed
read-only, resolving source symlinks so Docker/systemd DNS files and distro trust
bundle aliases work. The rest of `/etc`, private keys, scheduler data and process
environment secrets remain hidden. This change does not add network filtering;
connections use the same network reachability as the service.

Regression checks exercise an HTTP POST using name resolution and an HTTPS POST
with certificate verification against local test servers, compare the mounted
resolver/trust files with the service files, and retain all process cleanup,
filesystem, credential and resource-budget tests. No external API is contacted by
these tests.
