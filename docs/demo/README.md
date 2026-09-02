# Conduit terminal demo

This deterministic demo starts two safe local servers: a calculator and a
deliberately blocked admin server. It makes no network requests and reads no
credentials.

## Run it

From a checkout with Conduit installed (`pip install -e .` or `pip install
conduit-gateway`):

```sh
./docs/demo/run-demo.sh
```

Expected sequence:

```text
ready: True | healthy downstreams: admin, calc
published tools: calc.add, calc.multiply
result: 42
blocked: -32010 tool unavailable
tool_call_authorized: calc.add
tool_call_completed: calc.add
tool_call_denied: admin.reset
```

`admin.reset` is absent from discovery because the configured deny rule wins;
the final call confirms it remains unavailable and is recorded as denied. The
script cleans up both local servers and the temporary audit log on exit.

## Record a GIF

If [VHS](https://github.com/charmbracelet/vhs) is installed, render the checked
in tape from the repository root:

```sh
vhs docs/demo/conduit-demo.tape
```

This writes `docs/assets/conduit-demo.gif`. Review the rendered GIF before
committing it and keep it only if the output is readable and reasonably sized.
The tape enables `CONDUIT_DEMO_PAUSE=1` to hold each result on screen, producing
a roughly 20–30 second recording; ordinary demo runs remain fast.
