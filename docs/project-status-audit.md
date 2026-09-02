# Conduit project status audit

Audit date: 2026-08-31. Local audit revision: `main` / `v0.2.0` at `5c36791`. Public artifact: PyPI `conduit-gateway==0.2.0`. Overall readiness: **6.8/10 (68% complete)**.

## Executive summary

Conduit has a working constrained MCP federation core. The public wheel installed and interoperated with official `mcp==2.1.1` at MCP `2026-07-28`; it federated two real Streamable HTTP SDK servers, namespaced duplicate tools, enforced policy, isolated credentials, audited authorization before a verified side effect, avoided retries, degraded/recovered a downstream, and shut down/restarted cleanly.

It is **not production-ready for external users today**. A catalog response exceeding the configured byte limit kills its refresh task and leaves the gateway permanently `starting`. The public quick start refers to `conduit.example.yaml`, which is not in the wheel/sdist. Remote `main` lacks a release action-pin correction present in local/tagged v0.2.0, so future releases from remote main are unsafe.

No application source was changed. Clean-user harnesses were created outside the repository at `/private/tmp/conduit-public-audit.QR975n` and used only PyPI packages.

## Current project state

- Inventory: 16 modules in `src/conduit`, 66 pytest tests, MkDocs documentation, standard OSS files, CI/release workflows, and no `examples/` directory (though `MANIFEST.in` prunes one).
- Metadata: `conduit-gateway 0.2.0`, Python >=3.11, `conduit` console command.
- Worktree: `git status --short` was clean.
- local `main`/`v0.2.0`: `5c36791`, with corrected release pins.
- `origin/main`: `e1f1ad9`, without that correction; `origin/python-migration`: `1c58c26`.
- Public v0.2.0 tag points to `5c36791`; GitHub release was published 2026-08-26 with four assets.
- GitHub history had two failed v0.2.0 Release runs before one success on the corrected tag. This proves remote main differs from the successful release state.

Complete foundations: strict config/loopback controls; immutable catalog and routes; deterministic names; deny-wins/default-deny; 0600 durable audit; bounded redirect-disabled transport; one-shot dispatch; PyPI CLI package; and baseline CI.

## Feature-status matrix

| Feature | Status | Evidence / limitation |
| --- | --- | --- |
| MCP 2026-07-28 | Complete | Public wheel worked with official mcp 2.1.1. |
| server/discover | Complete | Official SDK connected through real `/mcp`. |
| tools/list | Partial | Aggregate works; public cursor is rejected with -32602. |
| tools/call | Complete | Real no-arg/string/numeric/structured/error/delay calls passed. |
| Multiple downstreams | Complete | Two official Streamable HTTP SDK servers, 10 tools each. |
| Deterministic namespacing | Complete | `a.duplicate` and `b.duplicate` independently routed. |
| Route mapping | Complete | Exact registry routes; no public-name splitting for dispatch. |
| Allow/deny policy | Complete | `a.*`/`b.*` allowed and `a.hidden` denied. |
| Default deny | Complete | Empty policy gave ready zero-tool catalog. |
| Credential/header isolation | Complete | Captured headers contained only owner credentials; no client cookie/Authorization. |
| Audit before dispatch | Complete | Downstream verified authorization record before side effect. |
| Audit failure behavior | Partial | Unsafe `/dev/full` audit target stopped startup; no safe post-start disk-full test. |
| No automatic retries | Complete | Timed-out call made one downstream call and returned -32012. |
| Tool response limits | Complete | 4096-byte response under 256-byte cap returned -32012. |
| Downstream catalog limits | Broken | Oversize response kills refresh task, permanent starting. |
| Downstream pagination | Complete | Two-page cursor behavior/limits unit-tested. |
| Public list pagination | Partial | Deliberately unsupported. |
| Refresh/degrade/recovery | Complete | 200ms refresh; kill A degraded only A; restart restored route. |
| Health/readiness | Partial | Normal/degraded/default-deny correct; cap failure leaves starting. |
| JSON-RPC errors | Complete | Failing tool is SDK error; tests preserve code/message/data. |
| Session handling | Partial | Source e2e covers DELETE; public SDK servers were stateless. |
| SSE behavior | Partial | Code rejects -32013; only mocked source test. |
| Redirect handling | Complete | Actual 302 not followed; server degraded. |
| Shutdown | Partial | SIGTERM/restart passed; no mid-call race. |
| Raw request ID | Partial | Source covers escaped/exponent/large; public probe large numeric only. |
| Timeout/concurrency | Complete | 1s/500ms timeout -32012; six 150ms calls finished <650ms. |
| Malformed downstream | Complete | Invalid tool schema degraded only that server. |
| Unknown tool/duplicates | Partial | -32010 no dispatch; cross-server duplicate passed, same-catalog source-only. |
| Audit terminal failure | Insufficiently tested | No deterministic runtime test. |
| Policy edge cases | Partial | Exact/wildcard/deny/default only; no fuzz/property tests. |
| x-mcp-header parity | Broken | Validator exists but ingress never invokes it. |

## Bugs found

### P0: catalog byte cap terminates refresh task

Reproduction used public PyPI Conduit plus official SDK servers with `max_downstream_catalog_bytes: 1`. After five seconds status was live=true, ready=false, aggregate state=starting/generation=0, and both downstreams state=starting.

`read_bounded` raises `ResponseTooLarge`. `Runtime._refresh_loop` at `src/conduit/app.py:55` catches CatalogError, ValueError, OSError, TimeoutError and httpx.HTTPError, but not that exception. The task exits before marking degraded or retrying. This violates documented recovery behavior.

Fix: catch ResponseTooLarge, preferably normalize transport/catalog failures to CatalogError, publish degraded state, retry, and regression-test a later healthy recovery.

### P1: installed quick start references nonexistent artifact

README/getting-started say `cp conduit.example.yaml conduit.yaml`. The clean PyPI environment had no file by that name, and fresh wheel/sdist inspection confirmed it. `MANIFEST.in` does not include the root template. Package a template plus initializer, or provide full copy/paste YAML and immutable raw link; add wheel-install documentation smoke coverage.

### P1: remote main lacks release correction

Remote main uses old PyPI-publish and GitHub-release action pins; local/tagged `5c36791` contains correction at `.github/workflows/release.yml:29`. The old version corresponds to two failed release runs. Merge correction and runtime fixes into **remote main**, verify with `git ls-remote`, then validate remote-main workflow before another tag.

### P2: header validator is dead production code

`headers.validate_call_headers` is implemented/tested but has no production caller. Ingress checks only Mcp-Name at `src/conduit/ingress.py:92` and accepts contradictory/unexpected Mcp-Param headers. Dispatch regenerates downstream headers, so no credential leak was observed, but the advertised protocol validation is not enforced.

## Public installation and end-to-end results

All commands ran from a fresh external directory without source imports, editable install or PYTHONPATH:

    python3 -m venv /private/tmp/conduit-public-audit.QR975n/venv
    venv/bin/python -m pip install conduit-gateway mcp
    venv/bin/python -m pip show conduit-gateway mcp
    venv/bin/conduit --help
    venv/bin/python public_e2e.py

Installed versions were `conduit-gateway 0.2.0` and `mcp 2.1.1`. The harness launched two official MCPServer Streamable HTTP servers and PyPI's CLI, then connected official `mcp.client.client.Client` to real localhost `/mcp`. Tools included no-argument, string, numeric, structured, error, delay, large-response, side-effect and duplicate names.

Successful result: PASS; live=true, ready=true, audit_healthy=true; aggregate generation=6/state=ready/tool_count=19; A and B healthy with 10 tools each; audit_records=18; events=49.

Verified: both downstreams discovered; names/routing; arguments/results; correlated errors; `/healthz` and `/status`; audit ordering; no retry; timeout; concurrency; credential isolation; degradation and recovery. A separate edge harness exercised default deny, response/catalog limits, malformed catalog, redirect, audit-startup failure and shutdown/restart. All designed paths passed; catalog-limit retained the reproduced product failure.

## Packaging, CI, and release readiness

The first sandboxed pytest attempt could not bind its loopback mock server (PermissionError): environment-only, not a Conduit failure. Unrestricted checks passed:

- `python -m pytest --cov`: 66 passed; coverage 85.00%.
- `ruff check .`: all checks passed.
- `mypy src`: success, 16 source files.
- `python -m build`: wheel and sdist built.
- `twine check dist/*`: both passed.
- `python -m pip_audit`: no known vulnerabilities.
- `mkdocs build --strict` and `git diff --check`: passed.

Coverage reaches only its floor: dispatch.py 71%, protocol.py 80%, config 84%; P0 lacked a test. CI uses SHA pins and runs tests/ruff/mypy/build/twine/pip-audit plus strict docs. Tag release omits pip-audit and strict docs and has no SBOM or Release Please workflow, despite [releases.md](releases.md:13) claiming both.

## Documentation usability review

Users can understand purpose, local-only scope, YAML shape, namespacing, policy, credential ownership, health endpoints and basic error codes. Problems:

1. README says both after v0.2.0 is published and until publication, though it is public.
2. The first copy-template command fails after installation.
3. No runnable client/agent configuration, local downstream, or first tools/list/tools/call validation command.
4. SUPPORT, compatibility and troubleshooting retain v0.1 wording although active package is 0.2.0.
5. Release docs promise absent Release Please/SBOM behavior; CHANGELOG calls public 0.2.0 Unreleased.
6. Historical Go/Phase-0 wording remains migration technical debt and confuses active product identity.

## Priority-ranked remaining work

1. Fix P0 exception containment and test degraded/recovery behavior.
2. Put corrected workflow on remote main before a tag.
3. Ship runnable config template and test README from empty installed environment.
4. Make release workflow/docs truthful: add/remove Release Please/SBOM, run audit/docs on release, date CHANGELOG.
5. Invoke header parity validation in ingress and test public mismatches.
6. Add real tests for stateful sessions, SSE, mid-call shutdown, raw IDs, terminal audit failure, limits, same-catalog duplicates and policy fuzzing.

## Required audit-area conclusions

### What is already complete

The public gateway's narrow discovery/list/call path, namespaced routing, deny-wins policy, per-downstream credential isolation, durable authorization-before-side-effect, bounded call responses, one-shot timeout behavior, healthy-peer continuity, refresh recovery, and Python packaging baseline are complete enough for controlled evaluation.

### What is partially complete, missing, and broken

Partial: public catalog pagination, live stateful session/SSE/shutdown-race evidence, raw ID integration coverage, policy edge coverage, and release-job parity. Missing: packaged example/init command, Release Please/SBOM despite documentation claims, and CI real-user integration coverage. Broken: catalog byte-cap task handling, public quick start, remote-main release pins, and unconnected header-parity validator.

### Multi-downstream, policy, security, audit, and failure results

The two-server SDK test passed: duplicate raw names did not collide; exact routes carried all argument types and result values; deny beat allow and default deny hid everything; A/B configured credentials never crossed and client credentials/cookies never propagated; authorization was present before a downstream side effect; one server failure left the other usable and restart recovered A. Redirects and malformed catalogs degraded only the bad server. Tool response overflow and call timeout yielded uncertain-after-dispatch with no retry. Catalog overflow is the exception: it exposed P0.

### Risks and remaining technical debt

The P0 task crash is an availability risk triggered by a defensive limit. The remote-main/tag discrepancy is a supply-chain/release risk. Exact 85% coverage masks low coverage in dispatch/protocol and missing fault-path tests. Stale Go/v0.1 and release claims increase operator error risk. Unsupported aggregate pagination, SSE/progress, OAuth, identity, and remote binding are intentional scope boundaries but need clearer user-facing expectation setting.

### Recommended tests to add

Add real SDK-driven tests for catalog overflow then recovery, stateful session cleanup, downstream SSE, redirect chains, malformed/correlated error bodies, call cancellation during shutdown, terminal audit failures, exact escaped/exponent IDs, header parity, concurrent refresh/call races, aggregate limit failures, and same-catalog duplicate names. Run the PyPI-wheel test with no checkout files in CI.

### Recommended documentation changes

Ship a template or `conduit init`; replace stale publication/v0.1 language; add a runnable downstream plus agent/client endpoint example; include first health/list/call verification commands; document exact degraded status interpretation and timeout uncertainty; correct or implement SBOM/Release Please claims; date CHANGELOG 0.2.0.

## Exact checklist before next release

- [ ] Catch ResponseTooLarge/catalog transport failures; assert degraded state and recovery.
- [ ] Add PyPI/official-SDK multi-process scenario to CI.
- [ ] Merge valid action pins into remote main and verify SHA before tag.
- [ ] Test remote tag/OIDC; include pip-audit, strict MkDocs and actual SBOM or correct docs.
- [ ] Ship/initialize template and execute README from empty wheel-installed directory.
- [ ] Correct versions, release claims and dated CHANGELOG.
- [ ] Enforce or remove unused Mcp-Param contract.
- [ ] Re-run supported-Python and clean-public-install tests.

## Clear answers

**Is Conduit ready for real external users today?** **No.** It is usable for controlled evaluation, but P0 refresh handling, broken installed quick start and remote-main release inconsistency are public-release blockers.

**What is still left before complete?** Fix those blockers, align release automation/docs/package artifacts, connect header validation, and add the real-world lifecycle/failure coverage above. The safety-focused core is promising, but not yet a complete production release.

## Remediation update — 2026-08-31

All four blocking audit findings were remediated and revalidated without changing the public protocol scope.

| Issue | Root cause | Fix | Regression test | Validation result | Remaining risk |
| --- | --- | --- | --- | --- | --- |
| Catalog response overflow | `ResponseTooLarge` escaped `Runtime._refresh_loop`, ending its background task. | `app.py` now catches it on every refresh and publishes a degraded snapshot before retrying. | `tests/test_app.py` verifies degrade, live task, ready recovery; clean wheel harness injects one oversized response then a valid catalog. | PASS: gateway stayed live, healthy peer remained usable, bad downstream recovered. | Other unexpected programmer exceptions still surface rather than being hidden. |
| PyPI quick start | Template existed only at repository root and was not wheel package data. | Package-owned `conduit/conduit.example.yaml`, setuptools package data, sdist include rules, and non-overwriting `conduit --init --config`. | CLI unit test checks private mode/no overwrite; wheel/sdist contents inspected. | PASS: fresh wheel venv ran `conduit --init`, created mode 0600 file, and refused overwrite. | Users must still edit downstream URL/credentials intentionally. |
| Release workflow main state | Correct action SHAs were in a tag/local head, not remote default branch; tag job also omitted parity checks. | The remediation commit contains the successful pinned SHAs; release job now installs docs and runs pip-audit and strict MkDocs. Release docs now match actual workflow. | `actionlint .github/workflows/*.yml`; compared release configuration with successful v0.2.0 pins. | PASS locally. Protected remote main requires PR #8 and its seven required checks before merge. | OIDC/PyPI publishing itself can only be validated by a future authorized release workflow run. |
| MCP parameter headers | Validator had no ingress/dispatch caller. | Dispatcher receives incoming headers and invokes `validate_call_headers` before authorization/transport. | Public-boundary e2e covers valid, missing, malformed, conflicting, unexpected headers and raw large-ID error fidelity. | PASS; valid existing non-annotated SDK clients remain compatible. | Current deliberate error mapping is `-32010` unavailable rather than a new public error code. |

Additional remediation tests cover malformed JSON-RPC catalog replies, repeated cursors, page caps, active-call shutdown with unknown-after-dispatch audit, redirects, unavailable/recovered downstreams, response limits, audit startup failure, and no call retry.

### Final validation after remediation

- Local quality suite: `python -m pytest --cov`, Ruff, mypy, pip-audit, build, Twine, strict MkDocs, actionlint, and `git diff --check` all passed.
- Artifact check: the generated wheel and sdist both contain `src/conduit/conduit.example.yaml`/`conduit/conduit.example.yaml` as appropriate and pass Twine.
- Clean external test: a brand-new temp venv installed only the built wheel plus official `mcp`; PyPI-style `conduit --init` flow, two SDK downstream harnesses, policy/security/audit/fault cases, and the literal documented demo walkthrough passed.

### Recalculated status

**Completion: 88%. Readiness: 8.8/10.** Conduit is now suitable for real external users within its documented local-first, terminal-JSON, Streamable-HTTP-only scope. A v0.2.1 release is recommended after the remediation commit is reviewed and CI completes. Remaining work before a broader “complete” claim is non-blocking: stateful-session/live-SSE integration coverage, more shutdown/audit-failure fault injection, aggregate list pagination (if product scope expands), richer policy fuzzing, and automated SBOM/Release Please only if maintainers decide to promise those features.

## Final hardening round — 2026-09-01

This section preserves the earlier audit and remediation record above, then
records the final targeted hardening work on `test/final-hardening`, based on
`main` at `91893c0` (`v0.2.1` is already tagged). It covers the three gaps
called out in the previous audit: stateful downstream/SSE lifecycle behavior,
audit fault injection, and policy/routing bypass resistance.

### Stateful sessions and SSE

| Previous gap | Tests and result | Bug / fix | Remaining risk |
| --- | --- | --- | --- |
| Only a mocked SSE path and one session-cleanup happy path existed. | `tests/test_stateful_streaming_integration.py` drives two independent real HTTP Streamable-HTTP-style downstreams through Conduit's actual `/mcp` endpoint. It verifies fresh invocation-owned session creation, endpoint-scoped cleanup, no A/B token crossover, stateful restart/invalidation safety, 16 concurrent calls, terminal SSE/progress rejection, malformed SSE, mid-body disconnect, stream overflow, recovery, malicious public-name rejection, and runtime shutdown during a delayed call. All passed. | A session ID received in response headers was lost if body reading later raised `ResponseTooLarge` or an HTTP read error, so cleanup was skipped. `transport.py` now attaches the invocation-owned ID to that exception; `dispatch.py` recovers it in the failure path and performs its one cleanup DELETE. | Conduit deliberately does not implement persistent client-owned downstream sessions or arbitrary SSE/progress. A downstream requiring an initialize/session token before every call is outside the documented scope. |

### Audit fault injection

| Previous gap | Tests and result | Bug / fix | Remaining risk |
| --- | --- | --- | --- |
| Audit-before-dispatch was demonstrated only on the happy path; short writes, lost paths, flush/fsync and cancellation were untested. | `tests/test_audit_hardening.py` injects unwritable startup, `PermissionError`, `ENOSPC`, `EIO`, deleted target, short write, serialization, flush and `fsync` failures; concurrent successful/failed calls; recovery by restart; and shutdown while authorization is deliberately stalled. It proves failing authorization produces `-32014` and zero downstream side effects, while a downstream side effect sees its authorization record first. All passed. | `AuditLog.append` ignored a short write, and an already-open Unix descriptor stayed writable after its pathname was removed/replaced. Both could falsely authorize a side effect without a durable record at the configured destination. It now checks write length and verifies inode/link/permissions before every record. A cancellation during pre-dispatch authorization could also reference an unbound prepared route; dispatch now returns a safe pre-dispatch failure with no transport start. | Audit intentionally fails closed until Conduit is restarted after the destination is repaired. This is documented operational behavior, not automatic recovery. A process crash between write and fsync remains correctly treated as no confirmed authorization. |

### Policy fuzzing and route identity

| Previous gap | Tests and result | Bug / fix | Remaining risk |
| --- | --- | --- | --- |
| Policy tests covered normal exact/wildcard rules only; raw catalog names were merely nonempty. | `tests/test_policy_fuzz.py` includes the requested punctuation, whitespace/control, Unicode/confusable, traversal/injection and long-name corpus plus 600 deterministic Hypothesis examples. It checks deny-wins/default-deny/case semantics, exact namespace routes, duplicate raw names, invalid config patterns, and that an invalid or unknown name cannot prepare or dispatch. All passed. | A raw downstream name with unsafe header characters or extra namespace separators could enter a catalog. Downstream IDs, raw names and exact/wildcard policy rules now share a strict component grammar: ASCII letters/digits/hyphen/underscore; ID 1–64; tool 1–128; exactly one Conduit separator. Catalog and registry validate defensively. | This intentionally rejects unusual MCP tool names rather than attempting Unicode/percent-decoding canonicalization. It is a documented compatibility boundary and avoids policy/routing disagreement. |

### Final validation evidence

Commands were run from this branch after the fixes:

```text
.venv/bin/python -m pytest --cov
137 passed, total coverage 87.84%

.venv/bin/python -m ruff check .
All checks passed

.venv/bin/python -m mypy src
Success: no issues found in 16 source files

.venv/bin/python -m pip_audit
No known vulnerabilities found

.venv/bin/python -m build
.venv/bin/python -m twine check dist/*
All wheel/sdist checks passed; wheel and sdist contain conduit.example.yaml

.venv/bin/mkdocs build --strict
Documentation built successfully

actionlint .github/workflows/*.yml
Passed
```

The strict MkDocs run emitted Material's upstream MkDocs-2.0 migration notice
and its existing “project-status-audit.md is not in nav” information message;
neither is a build failure.

For the artifact-only check, a fresh directory outside the repository created a
new virtual environment and installed only
`dist/conduit_gateway-0.2.1-py3-none-any.whl` plus official `mcp==2.1.1`:

```text
venv/bin/conduit --init --config conduit.yaml  # created mode 0600 file
venv/bin/python public_e2e.py
PASS: ready=true, audit_healthy=true, two official SDK Streamable HTTP servers,
19 policy-visible namespaced tools, audit_records=18, events=51
```

That end-to-end run used the installed CLI and an official MCP client against
the real public `/mcp` endpoint. It covered discovery, `tools/list`, all tool
argument forms, duplicate names, downstream errors, policy deny, audit ordering,
timeout/no retry, concurrent calls, configured-header isolation, one-downstream
degrade/recovery, and clean shutdown. It did not import the source checkout or
use an editable installation.

### Recalculated status

**Completion: 94%. Readiness: 9.4/10.** There are **no known functional
blockers** before calling Conduit complete for its intended constrained v0.x
scope: local-first terminal-JSON federation with invocation-owned sessions and
fail-closed auditing. It is ready to be presented confidently as a public
project within that scope.

Before publishing another package, bump the project version: `v0.2.1` is
already an upstream tag, so these changes cannot be released under that version.
That is normal release bookkeeping rather than a gateway blocker. Future scope
work remains optional: public aggregate pagination, durable session pooling,
SSE/progress support, automatic audit-destination recovery, and CI execution of
the clean-wheel SDK harness.

## Client interoperability remediation — 2026-09-02

### Evidence captured before implementation

A disposable HTTP recorder outside the checkout captured installed clients
before the ingress change. This was intentionally wire-level evidence rather
than an assumption that all MCP clients use Conduit's native profile.

| Client | Observed sequence | Consequence for pre-change Conduit |
| --- | --- | --- |
| MCP Inspector 2.4.0 default | `POST initialize` with `params.protocolVersion: "2025-11-25"`, no `Mcp-Method` or native `_meta`; then `notifications/initialized`, `Mcp-Session-Id`, `GET /mcp`, and `tools/list`. | Rejected as `400 invalid MCP request`. |
| Codex CLI 0.149.1 | Initial GET/OAuth discovery probes, then `POST initialize` with `protocolVersion: "2025-06-18"`; subsequent notification, session GET, and `tools/list` with a session header. | Rejected as `400 invalid MCP request`. |
| MCP Inspector forced modern | Native `server/discover` / `tools/list` / `tools/call` `2026-07-28` profile. | Already passed and was retained unchanged. |
| Claude Code 2.1.236 | `claude mcp add --transport http` records `type: http`; local-scope `claude mcp get` performs an HTTP health connection and reported `Status: Connected`. | Project-scope connections await Claude approval; a model tool call requires Claude authentication. |

The recorder captured relevant method, request JSON, `Accept`, content type,
protocol, session, and user-agent headers. Codex also probed OAuth discovery
paths before initialization; Conduit continues to return no OAuth metadata,
which is correct for its documented no-OAuth-broker scope.

### Fix and safety boundary

`protocol.py` now has a framing-only standard request validator for the two
observed negotiated versions; `compatibility.py` owns opaque public client
session IDs; and `ingress.py` adapts only `initialize`,
`notifications/initialized`, `tools/list`, and `tools/call`. The native modern
validator remains first and unchanged. A malformed request explicitly marked
as modern cannot fall through to compatibility.

The standard adapter requires an opaque, version-bound `Mcp-Session-Id` after
initialization, provides the session GET endpoint with a terminal comment-only
SSE content type for clients that establish an event channel, and deletes the
session on `DELETE /mcp`. It never forwards that client session downstream. It
normalizes only the internal `Mcp-Name` correlation header from the verified
body; all policy, safe-name checks, parameter-header validation, durable audit
write, exact route selection, credential isolation, timeout, and no-retry
behavior still run in the existing dispatcher.

The first implementation returned `204` for the standard session GET. A real
Codex connector reported `Unexpected content type: None`; the fixed endpoint
returns `text/event-stream` with a comment and no event/tool payload. The
second real Codex run no longer produced that MCP transport error. This is a
real interoperability bug fixed by the adapter, not a relaxation of
downstream SSE/progress policy.

### Regression and real-client validation

`tests/test_e2e.py` now exercises both negotiated versions through actual
`/mcp` requests against two independent HTTP downstreams. It proves standard
initialize/notification/session GET/list/call/delete, raw numeric request-ID
fidelity, x-mcp-header enforcement, audit-before-side-effect, denial,
conflicting routing-header rejection, session version binding, and session
cleanup. Existing native modern tests remain unchanged.

Real Inspector 2.4.0 validation against a live Conduit process passed both
default and forced-modern `tools/list` and `tools/call`; the latter returned
the downstream echo payload. `claude mcp get conduit` reported `Connected` for
the required HTTP add command. Codex configured the remote MCP URL and opened
the compatible MCP session without a transport error.

The environment used for this audit has no Claude login and its isolated Codex
CLI home has no OpenAI credential. Consequently the final model-directed
Claude Code and Codex `tools/call` commands stop at their vendors' respective
authentication gates before either model can choose a tool. This is an
environment-only validation limitation, not evidence of a Conduit request
failure; it is explicitly documented rather than disguised as a passing call.

### Updated assessment

The compatibility adapter removes the known `400 invalid MCP request` blocker
for the actual negotiated Inspector and Codex request shapes, and Claude Code's
real HTTP initialize/list sequence now connects. The remaining release-validation action is
to rerun the documented Claude/Codex model tool-call smoke tests in an
authenticated account before marketing those two calls as independently
verified. This is a release confidence requirement, not a known gateway
runtime defect.

### Final commands and recalculated status

After the compatibility fix, the local quality suite completed with **141
passed** tests and **87.36%** total coverage. Ruff, strict mypy, pip-audit,
strict MkDocs, actionlint, `python -m build`, Twine, and `git diff --check`
all passed. The newly built wheel and sdist explicitly contained
`conduit/compatibility.py` and the packaged quick-start template.

A fresh external virtual environment installed only the built
`conduit_gateway-0.2.2-py3-none-any.whl` and `mcp==2.1.1`; it ran
`conduit --init --config quickstart.yaml` successfully at mode 0600. Its two
independent official SDK downstream servers (`github` and `calc`) were then
exercised with real Inspector default and forced-modern clients. Default
Inspector listed `github.add`, `github.echo`, `github.search`, `calc.square`,
and `calc.search`, hid denied `github.multiply`, and called `github.add` and
`calc.square` correctly. Forced-modern Inspector called `calc.search` through
the unchanged native path. The audit log contained all three authorizations
before dispatch, and captured downstream headers showed the configured
credential remained scoped to its owning server.

**Completion: 96%. Readiness: 9.5/10.** There is no known Conduit runtime
blocker for its intended local-first HTTP scope. Before a release announcement
claims full Claude/Codex end-to-end tool-call proof, run the documented
authenticated smoke calls; this audit environment could prove their actual
connections/negotiations but not authorize their model endpoints. The
compatibility feature warrants a **minor v0.x release** rather than reusing the
already-published `0.2.2` artifact.

## Terminal downstream SSE compatibility — 2026-09-02

### Root cause and remediation

The downstream transport treated every `text/event-stream` response as an
unsupported tool response and exposed its raw SSE bytes to catalog parsing.
GitHub's official MCP endpoint correctly returns finite terminal JSON-RPC
messages in SSE (`event: message` / `data: ...`) even for modern
`server/discover`; Conduit therefore degraded a valid downstream as a catalog
failure.

`transport.py` now performs content-type dispatch: ordinary
`application/json` retains the existing bounded decoder, while
`text/event-stream` uses a bounded incremental terminal-SSE decoder. It accepts
only one complete correlated JSON-RPC response, consumes the finite stream to
detect a second response, and returns the decoded JSON bytes to the unchanged
catalog/dispatcher path. CRLF/LF, comments, chunk boundaries, and multi-line
data framing are supported. Progress/unknown events, malformed or non-JSON
data, truncated streams, unmatched IDs, multiple terminal messages, wrong
content type, and size overflow fail closed; a post-dispatch transport loss or
overflow remains the existing unknown tool outcome with no retry.

### Deterministic and remote validation

New transport and real-local-HTTP tests cover valid JSON/SSE catalog and tool
responses, arbitrary chunk boundaries, LF/CRLF, comments, data-line joining,
wrong IDs/types, unsupported events, malformed/non-JSON/truncated/no-terminal
streams, multiple terminal messages, unexpected content type, limits, iterator
closure on cancellation, session cleanup, degraded-peer isolation, and recovery.

Using a local credential supplied only from `gh auth token` (never printed,
committed, or retained in repository configuration), the live GitHub endpoint
`https://api.githubcopilot.com/mcp/` returned `200 text/event-stream` to the
modern discovery probe. Through Conduit, `githubremote` became **healthy** with
**14** discovered tools. `tools/list` exposed all 14 namespaced tools; actual
catalog inspection selected `githubremote.get_latest_release` with required
`owner`/`repo` header-annotated arguments. A read-only `cli/cli` call completed
through Conduit with HTTP 200, terminal result, and audit authorization before
completion.

The isolated Codex CLI remote-MCP configuration connected to Conduit but its
model execution exited 1 before tool selection because the environment has no
OpenAI/Codex authentication (`401 Unauthorized`). This is an external
credential limitation; the same read-only call succeeded directly through
Conduit and GitHub with the real discovered schema. An authenticated Codex
account must rerun the documented `githubremote.get_latest_release` smoke call
before claiming a model-directed Codex call has been independently verified.

### Final validation and status

The complete post-change suite passed: `python -m pytest --cov` reported **153
passed** and **87.89%** total coverage. Ruff, strict mypy, pip-audit,
`python -m build`, `twine check dist/*`, strict MkDocs, actionlint, and `git
diff --check` all passed. A newly created external virtual environment then
installed only the built `conduit_gateway-0.3.0-py3-none-any.whl`; `conduit
--init --config quickstart.yaml` succeeded, and a separate local HTTP server
which requires both MCP `Accept` media types proved terminal-SSE
discovery/list/call plus audit ordering through the installed wheel.

**Updated assessment: 97% complete, 9.6/10 readiness for the intended local
gateway scope.** No known Conduit runtime blocker remains for finite terminal
SSE downstreams. The outstanding release-evidence limitation is the
environment's unauthenticated Codex model account, not gateway behavior: an
authenticated Codex smoke call against the documented GitHub configuration is
still required before representing that specific end-to-end client proof as
complete. This interoperability repair warrants a **v0.3.1** patch release.
