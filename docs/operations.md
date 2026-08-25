# Operations

`GET /healthz` and `GET /status` expose liveness, readiness, audit health,
aggregate generation, and sorted downstream state. Readiness requires a live
process, healthy audit storage, completed initial refreshes, a usable aggregate,
and at least one healthy downstream.

Shutdown rejects new dispatches, cancels active calls, waits for terminal audit
handling, then closes transport and audit storage.
