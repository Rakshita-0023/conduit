# Go-to-Python migration parity contract

The Go v0.1.x implementation is the behavioral oracle for a future Python
implementation. Python runtime work has not started. The scenarios in
[`testdata/parity`](../testdata/parity) and the Go public-boundary oracle test
are the compact executable contract; the existing package tests remain the
authoritative detailed specification.

Each oracle observation contains public HTTP/MCP responses, selected downstream
requests, ordered audit events, and readiness/status snapshots. Timestamps,
generated call IDs, listener ports, durations, and build versions are
normalized. Request/response ID correlation, tool order, route destinations,
request counts, routing headers, audit order, and error codes are not
normalized.

## Ingress and discovery

| Scenario | Expected public result | Go reference | Parity |
| --- | --- | --- | --- |
| Valid 2026 request | `server/discover` advertises only 2026-07-28 and tools capability | `TestDiscoverAndProtocolAdmission`, `TestDiscoverAdvertisesExactProfile` | Exact except build version |
| Invalid body, headers, metadata, or request ID | Rejected before execution; valid unsupported methods return HTTP 404 / JSON-RPC -32601 | ingress admission tests | Exact status, code, and ID behavior |
| Not ready | `/healthz` remains inspectable; MCP discovery/list/call is HTTP 503 | `TestHealthAndNotReady` | Exact |

## Registry, federation, and policy

| Scenario | Expected public result | Downstream/audit observation | Go reference | Parity |
| --- | --- | --- | --- | --- |
| Deterministic order | `tools/list` sorts allowed public names | No call required | `TestOrderingDoesNotDependOnPublicationOrder` | Exact tool order |
| Exact route | `a.b.c` maps to server `a.b`, tool `c` | One call has downstream name `c` | `TestRouteIsStoredNotReconstructedFromPublicName` | Exact |
| Deny wins/default deny | Hidden tools are absent and calls return -32010 | Zero tools/call POSTs | `TestPolicyFilteringAndEmptyAggregate`, `TestDeniedAndUnknownNeverDispatch` | Exact |
| Collision/aggregate limit | No partial aggregate is exposed; state is unavailable/collision/over-limit | No route is executable | registry and ingress aggregate-limit tests | Exact state/code |
| Generation replacement | Readers see complete generations only; stale prepared routes are never authorized | No stale POST | `TestAuthorizationLinearizationBlocksPublicationWithoutDeadlock`, `TestGenerationChangeBeforeAuthorizationNeverDispatchesStaleRoute` | Exact behavior; generation number normalized only when implementation-specific |

## Refresh and readiness

| Scenario | Expected public result | Go reference | Parity |
| --- | --- | --- | --- |
| Initial refresh | Ready only after every configured downstream attempted, one is healthy, audit is healthy, and aggregate is ready | health tests, `TestStartIsLiveBeforeInitialRefreshCompletes` | Exact state predicates |
| Failure/removal/recovery | Failed refresh removes that catalog; later success restores it | `TestPeriodicRefreshRemovesStaleCatalogAndRecovers` | Exact tools/status transition |
| Refresh concurrency | A downstream has no overlapping refresh; stale health cannot overwrite a newer generation | focused app refresh tests | Python equivalent required later |

## Dispatch, errors, and audit

| Scenario | Expected public result | Downstream/audit observation | Go reference | Parity |
| --- | --- | --- | --- |
| Allowed call | Result retains raw tool payload fidelity | One POST to exact route, authorization before POST, completion audit | `TestOneInvocationIsOneToolCallWithoutHandshake` | Exact after call-ID normalization |
| Unknown/denied | -32010 | Zero POST; denied audit | dispatcher tests | Exact |
| Pre-dispatch failure | -32011 | Zero POST where transport is rejected before starting | dispatcher configured-session test | Exact |
| Post-dispatch uncertainty | -32012 | One POST; terminal unknown audit; no retry | malformed/reset/oversize tests | Exact |
| Unsupported terminal result | -32013 | One POST; unsupported terminal audit | `TestInputRequiredCompatibility` | Exact |
| Audit unavailable | -32014 before downstream side effect | Zero POST | `TestAuditFailureBeforeDispatchPreventsTransport` | Exact |
| Downstream JSON-RPC error | Preserve code/message/raw data, including bounded HTTP 400/404 error envelopes | One POST | dispatcher fidelity tests | Exact except outer client ID comes from scenario |
| Terminal audit failure | Preserve already-valid result, then reject future authorizations | Audit becomes unhealthy | `TestTerminalAuditFailurePreservesValidResultAndBlocksNewAuthorization` | Code-driven |

## Transport and sessions

| Scenario | Expected public result | Downstream observation | Go reference | Parity |
| --- | --- | --- | --- |
| Bounds | Exact byte limit succeeds; one byte over is -32012 | Oracle verifies those public outcomes | response-limit tests | Exact outcome; low-level bounded-read behavior remains covered by focused Go tests and needs a Python equivalent |
| Isolation | Caller headers/credentials and protected configured MCP headers do not control a downstream call | Only configured safe headers and Conduit routing headers reach destination | catalog/dispatcher tests | Exact selected headers |
| Redirect/no retry | Redirects and connection reset never cause a second tools/call | One POST only | redirect/reset tests | Exact count |
| Stateful session | A response-created session gets one same-endpoint DELETE using only its returned ID | One POST, one DELETE; stateless call gets no DELETE | session tests | Exact |
| SSE | SSE/progress is -32012 and is not relayed; a returned session is still cleaned up | At most one DELETE | SSE tests | Exact |

## Shutdown

Shutdown stops dispatch admission, stops HTTP acceptance, cancels active calls
and cleanup, waits through their terminal audit path, then closes audit storage.
An already-authorized invocation is not rolled back. A cancelled call that may
have reached transport is -32012 and is terminal-audited. These are code-driven
because they require deterministic blocking downstream handlers.

References: `TestCloseOwnsActiveDispatchTerminalAudit`,
`TestCloseReturnsAfterCancellationWithStubbornDownstream`, and
`TestShutdownCancelsStatefulCleanupWithoutReplacingResult`.

## Fixture scope

Fixture JSON covers normal, bounded, and single-request scenarios. Blocking
refresh, audit-failure, reset, and shutdown cases are intentionally code-driven
in `internal/parity/oracle_lifecycle_test.go`; forcing those into static data would hide
the synchronization that the migration must preserve.

Each fixture has this deliberately small shape:

```json
{
  "downstreams": [{"id": "github", "headers": {}, "tools": []}],
  "policy": {"allow": ["github.*"], "deny": []},
  "limits": {"max_tool_response_bytes": 1024},
  "call": {"name": "github.search", "arguments": {}},
  "expect": {
    "ready": true,
    "tools": ["github.search"],
    "call": {"status": 200, "error_code": -32012},
    "downstream": [{"id": "github", "method": "POST", "count": 1}],
    "audit_events": ["audit_ready", "tool_call_authorized"]
  }
}
```

`call_response` on a downstream is limited to normal terminal JSON results,
JSON-RPC errors, malformed bodies, and SSE responses. It can also set an HTTP
status and invocation-owned session ID. The runner records normalized public
responses, selected downstream routing headers/body, audit event order, and
status; it does not encode arbitrary server logic.
