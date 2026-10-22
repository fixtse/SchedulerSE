# Security fixes following the 22 October audit

The six findings in `security-audit-2026-10-22.md` are addressed as follows:

- SEC-01: Bash and agent Bash have independent default-off administrator switches.
  The native shell runner has been replaced with a fail-closed bubblewrap sandbox
  exposing only read-only commands/libraries, a private `/tmp`, private processes,
  and no network or inherited credentials. Existing saved Bash cannot bypass the
  execution adapter's disable switch.
- SEC-02: every shell is PID 1 of its own PID namespace, so both successful exit
  and cancellation destroy detached descendants. No process-group-only fallback
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
Bash by default and removes its previous network/environment/`/data` access even
when enabled. No live deployment or credentials were changed.

Security regression tests cover disabled execution, REST and MCP policy, host file
and environment isolation, network namespace separation, detached-child cleanup,
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
