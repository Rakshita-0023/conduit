# CLI reference

```text
conduit [-config path]
```

`-config` selects the YAML configuration file and defaults to
`conduit.yaml`. The process validates configuration, opens the audit log,
binds the configured loopback listener, and starts catalog refresh loops.

Stop the process with `SIGINT` or `SIGTERM`. Shutdown rejects new dispatch,
stops HTTP acceptance, cancels active dispatches, waits for terminal audit
paths, and then closes audit storage.

There are no daemon, credential, OAuth, stdio, replay, or management CLI
commands in v0.1.
