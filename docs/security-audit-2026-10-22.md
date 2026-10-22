# Security audit — 22 October 2026

Reviewed commit: `7c4d30079e3ad05180baa69b6031bb036e831a67`.

## Assessment

The service is **not an isolation boundary for agent-authored Bash**. An agent API token grants execution with the scheduler's own OS identity, environment, network access, and writable state. This is partly documented in the README, but materially exceeds reminder scheduling or even ordinary automation management: a script can access saved credentials, alter authentication state and execution history, or stop the service. Container hardening protects parts of the host; it does not protect the scheduler from its scripts.

Three high-severity findings and three medium-severity findings follow. The first is an architectural trust risk under the documented trusted-agent model; it becomes an authorization boundary failure if agents are intended to have less authority than the administrator. No unauthenticated remote code execution or container escape was demonstrated. Severity assumes a reachable installation, an agent that can be compromised or induced to misuse tools, and potentially sensitive scheduler credentials.

## Scope and method

Reviewed the Go entry point, REST/MCP routes, authentication and password recovery, SQLite persistence, scheduling and execution, HTTP and Telegram delivery, embedded browser UI, Python bridge, Cloudflare Worker, Docker/Compose configuration, and publication workflow. Inspected validation, credential handling, process lifetime, concurrency, and persistence boundaries.

Ran the existing race-enabled Go suite, Go vet, Python bridge suite, Compose validation, and a current Go vulnerability scan. Ran two temporary Go probes against the actual script runner using only synthetic secrets and temporary files; explicitly killed the detached probe process and removed the temporary test source afterward. Did not read `.env` contents, invoke production jobs, access production databases, or send external callbacks.

This is a source and local-execution audit, not a production penetration test. The deployed proxy/TLS configuration, actual mounts, host policy, running image contents, and Alpine package advisories were not verified. Go vulnerability scanning does not cover bundled SQLite C code or runtime APK packages. Browser behavior and Worker deployment were inspected in source rather than tested against live services.

## Findings

### SEC-01 — High: agent Bash shares the control plane's secrets and authority

Locations: `internal/adapters/script.go:26`, `internal/adapters/store.go:17`, `internal/adapters/web/agent.go:41`, `internal/adapters/web/agent_automations.go:30`, `compose.yaml:13`.

`exec.CommandContext` leaves `Env` unset and does not change credentials, namespaces, or filesystem access. Scripts therefore inherit the service environment and UID. `/data`, containing `runs.sqlite`, is mounted writable for that same UID. The database contains saved HTTP authorization headers, Telegram and webhook credentials, account password hashes, agent token hashes, sessions, schedules, and execution records. Redacting authorization fields from JSON responses does not protect them from a Bash caller. The included tools support outbound exfiltration; output suppression is not a confidentiality boundary.

An agent token can create and run Bash without a separate execution capability or administrator approval. Existing reminder-only integrations consequently gain shell authority when upgraded. The single shared token also prevents per-agent isolation or attribution. A script can tamper with state through the filesystem or available database-capable mechanisms, impersonate administrative state, erase evidence, or signal the service process. A native deployment exposes the privileges of whichever OS account starts it; the Dockerfile's non-root user does not apply outside Docker.

**Evidence:** a temporary runner probe inherited `AUDIT_FIXTURE_SECRET=synthetic-secret`, wrote it to a temporary file, and overwrote a service-owned fixture initially created with mode `0600`. The test passed. No real credentials were accessed. Database compromise is inferred from the same UID and writable database mount, not tested against production state.

**Remediation:** default agent Bash off and introduce separate reminder, HTTP automation, and shell capabilities, with per-client revocation. Run scripts in disposable workers with a separate identity and filesystem, no scheduler database mount, an explicit minimal environment, only task-specific secrets, and restricted network access. Do not mount the Docker socket into the scheduler as a shortcut. An environment allowlist alone is insufficient while the scheduler's files or process environment remain reachable.

**Containment implication:** token rotation blocks new API calls, but does not stop a running script or disable malicious saved cron jobs (`internal/adapters/web/agent.go:271`). After suspected misuse, stop execution, review saved jobs and state integrity, and rotate credentials exposed to the worker. Revocation should support disabling work associated with the revoked principal.

### SEC-02 — High: detached descendants survive the execution timeout

Location: `internal/adapters/script.go:29`.

Cancellation and normal cleanup signal only the original process group. A script can use `setsid` to create a different session and process group, so its child survives both timeout cancellation and the cleanup kill. `util-linux`, including `setsid`, is deliberately installed in the runtime. `WaitDelay` does not terminate such descendants.

**Evidence:** a temporary runner probe launched a detached `sleep 20`, recorded its PID, then exceeded a 200 ms script timeout. After `runScript` returned an abort, `kill(pid, 0)` still succeeded. The probe then explicitly killed the survivor. The relevant script structure was:

```bash
setsid bash -c 'echo $$ > "$AUDIT_FIXTURE_DIR/pid"; exec sleep 20' \
  </dev/null >/dev/null 2>&1 &
while [ ! -s "$AUDIT_FIXTURE_DIR/pid" ]; do sleep 0.01; done
sleep 10
```

**Impact:** jobs can continue network activity or consume resources after being reported aborted or complete. Repeated runs can accumulate persistent children. This bypasses the documented timeout expectation without requiring elevated privileges.

**Remediation:** place each run in its own enforced cgroup/container and terminate the entire group on completion or cancellation. Retain process-group cleanup as defense in depth. Removing `setsid` alone does not fix the boundary. Add a regression test that checks detached descendants disappear after both timeout and successful script exit.

### SEC-03 — High: shell execution has no effective resource budget

Locations: `compose.yaml:3`, `internal/adapters/web/server.go:648`, `internal/adapters/scheduler.go:112`, `internal/adapters/web/agent_automations.go:64`.

Compose sets no memory, CPU, or PID limit. The application starts a goroutine per admitted distinct job and has no global execution semaphore, per-principal quota, or job-count limit. The SQLite unique index prevents overlapping runs of the same job, but not many distinct jobs or process creation within one script. Timeouts do not bound memory/process spikes. The persistent state volume has no configured size quota, and execution/history records and bridge receipts have no general retention policy.

**Impact:** an agent mistake or hostile script can exhaust memory/PIDs, starve the scheduler, or fill persistent storage. Depending on the deployment's external limits, this can affect other host workloads. `/tmp`'s 16 MiB limit does not constrain `/data` or anonymous memory.

**Evidence:** source/configuration review; destructive fork-bomb, OOM, and disk-fill tests were intentionally not run.

**Remediation:** enforce per-worker CPU, memory, PID, runtime, and disk budgets; add a bounded queue and global execution limit; bound saved jobs and API creation rates; define storage quotas and retention that preserve required idempotency receipts. Container-wide limits are useful immediate host protection, but running workers separately is necessary to preserve scheduler availability.

### SEC-04 — Medium: changing a webhook destination silently reuses its old credential

Locations: `internal/adapters/web/agent.go:247`, `internal/adapters/web/agent.go:413`, `internal/adapters/agent.go:124`.

Both the browser form and agent connection API preserve `WebhookAuth` when no replacement is supplied, even when `WebhookURL` changes to a different origin. The next due webhook sends the preserved authorization header to that new destination. The API deliberately hides the saved credential from reads, but changing its destination provides an indirect disclosure path. It also makes ordinary configuration changes capable of leaking a credential accidentally. Similar destination/header coupling should be reviewed for editable HTTP jobs and fallback endpoints.

**Evidence:** direct dataflow review from URL update, through preserved auth, to the delivery POST. No external receiver was contacted. Exploitation requires permission to configure delivery; in today's shared full-shell token model SEC-01 already provides broader access, but this remains relevant when adding restricted scopes.

**Remediation:** bind credentials to the destination origin. Reject an origin change unless the caller explicitly clears or replaces the credential; never silently forward an existing secret to a new origin. Test this through both REST/MCP and browser forms.

### SEC-05 — Medium: the local bridge accepts remote plaintext transport for the full-power token

Location: `internal/agentkit/schedulerse-delivery/scripts/bridge.py:90`.

`validate_scheduler_origin` accepts arbitrary `http://` hosts, and `request_json` sends the shared agent bearer token to them. This permits a user or agent to configure plaintext transport across a LAN or other untrusted network. An on-path party can recover a token that now grants shell execution. The validator does not enforce the README's instruction to use HTTPS remotely. Loopback publishing in the supplied Compose file reduces default exposure but does not constrain bridge configuration or other deployment methods.

**Evidence:** validator and Authorization-header dataflow inspection. No real token was transmitted.

**Remediation:** require HTTPS by default; allow HTTP on loopback, and require a deliberately named insecure-network opt-in for trusted container-network deployments. Validate on initialization and each request. Add cases covering remote HTTP rejection and explicit local exceptions. Ensure the UI's session cookies are also marked Secure in HTTPS deployments; that setting currently defaults off.

### SEC-06 — Medium: unauthenticated callers can exhaust the shared sign-in budget

Locations: `internal/adapters/web/server.go:339`, `internal/adapters/web/password_reset.go:62`.

The five-attempts-per-minute limiter is global to the server and shared by sign-in, setup, and reset verification. An unauthenticated caller can obtain its own preauth cookie/form token and submit five invalid sign-ins each minute, causing valid administrator attempts to receive 429 responses. CSRF protection does not stop an attacker making requests in their own session.

**Impact:** sustained low-bandwidth denial of administrator login and reset verification. Existing authenticated sessions continue working. This requires network access to the login endpoint; the default loopback-only publication limits remote exposure unless proxied.

**Remediation:** combine bounded per-source/per-account throttles with an independent global concurrency budget for expensive password hashing. Avoid permanent account lockout and trust forwarded client IPs only from configured proxies. Add a test demonstrating that one abusive source does not deny a valid login from an independent source.

## Controls that held up in review

- Agent REST/MCP requests require a valid bearer token; browser sessions do not authorize the agent API. Token hashes are compared in constant time and rotation is checked per request.
- Browser mutations require a session and CSRF token; cookies are HttpOnly and SameSite Strict. Authentication uses bcrypt; reset tokens are hashed, expire, are consumed transactionally, and invalidate sessions.
- SQL values use placeholders. Templates use Go HTML escaping and the UI has a restrictive CSP. Delivery-file access is limited to embedded files with valid relative paths.
- Request bodies and HTTP response sizes are bounded. Outbound HTTP redirects are rejected, avoiding redirect-based credential forwarding.
- Manual runs require a saved revision and idempotency token, with durable claims and same-job overlap prevention. The production runner applies saved default timeouts before Bash execution; the direct adapter's zero-timeout behavior is not reported as a production timeout bypass.
- Compose uses a non-root UID, read-only root filesystem, dropped capabilities, no-new-privileges, loopback publication, and bounded temporary storage. These are useful host defenses, but do not isolate scripts from the scheduler.
- CI actions are pinned by commit and workflow permissions are scoped. `.env` is ignored and excluded by the Docker build context allowlist.

## Additional trust and maintenance observations

Outbound endpoint validation checks syntax and HTTPS, not resolved destination addresses. HTTP jobs, fallback requests, and callbacks may target loopback/private services; Bash has unrestricted egress as well. Treat this as intentional privileged network access today. If restricted agent roles are added, enforce an egress policy at connection time covering DNS resolution and both IPv4 and IPv6, plus a worker network policy. Blocking URL strings alone is insufficient.

Stored reminder text reaches an AI agent. The bridge frames it as untrusted data and passes it on stdin using an argv list rather than a shell command. Those controls reduce direct command injection, but prompt instructions are not authorization enforcement. The bridge's read-only Codex sandbox should not be assumed to remove the authority of configured remote tools; give the reminder consumer only narrowly scoped integrations. Custom command adapters inherit the chosen command's permissions.

The image and APK inputs use mutable tags/package resolution. Add image-level scanning and an SBOM for each release, including SQLite and Alpine packages; rebuild regularly. Prefer immutable deployed digests. The Go scan below is not evidence that the complete production image has no known vulnerabilities.

## Validation results

- `go test -race -timeout 60s ./...`: passed (existing suite, cached package results).
- `go vet ./...`: passed.
- `python3 -m unittest discover -s test -p 'test_agent_bridge.py'`: 11 tests passed; Python emitted resource warnings about test database/HTTPError cleanup.
- `docker compose config --no-env-resolution --quiet`: passed; environment secrets were not rendered.
- Temporary `TestAuditProbeEnvironmentAndState` and `TestAuditProbeDetachedChild`: passed uncached, confirming SEC-01 and SEC-02. Probe source removed after execution.
- `go run golang.org/x/vuln/cmd/govulncheck@latest -show verbose ./...`: no reachable vulnerabilities and no vulnerabilities in imported packages. One module-only advisory, `GO-2026-5932`, concerns unused `golang.org/x/crypto/openpgp`; the application does not import that package. Scan used local Go `1.27.1-X:nodwarf5`; the Dockerfile specifies Go `1.27.2`.

## Recommended order

1. Disable agent Bash by default or restrict the current token to fully trusted administrators pending worker isolation.
2. Separate workers from control-plane files, processes, credentials, and network authority; enforce per-run cgroup lifetime and resource budgets.
3. Add per-client scopes, ownership, audit attribution, and a revocation/kill workflow for saved and active work.
4. Fix credential reuse on destination changes, remote plaintext bridge transport, and the shared login limiter.
5. Add security regression coverage and image-level dependency scanning before declaring the release suitable for less-trusted agents.

No application code or deployment configuration was changed by this audit.
