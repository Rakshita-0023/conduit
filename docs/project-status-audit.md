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
