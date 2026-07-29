# mockgrbl developer guide

## Purpose

`cmd/mockgrbl` is a local GrblDD-style mock controller for DDGo development and Linux serial-tagged integration tests. It exposes a pseudo-terminal (PTY) that DDGo can open like a serial port, together with an HTTP debug API for inspecting state and injecting faults.

The mock is for development and tests, not real CNC control. It is intentionally narrow: it models only the firmware behaviors that DDGo currently needs rather than attempting to be a full firmware emulator.

## Running the mock

Start the mock with the `serial` build tag:

```bash
go run -tags serial ./cmd/mockgrbl
```

By default, the stable serial symlink is `/tmp/ddgo-mock-grbl` and the debug HTTP server listens on `127.0.0.1:8088`. The mock logs both the actual PTY path and the stable symlink path at startup. Select the stable path as DDGo's serial port.

For example, run the mock and DDGo in separate terminals:

```bash
go run -tags serial ./cmd/mockgrbl
go run -tags 'miqt serial' ./cmd/ddgo
```

## Command-line flags

- `-symlink`: sets the stable serial path that DDGo and tests can open. The default is `/tmp/ddgo-mock-grbl`.
- `-http`: sets the debug API listen address. The default is `127.0.0.1:8088`.
- `-response-delay`: adds a duration delay before every serial response line, useful for exercising timing and wait behavior.
- `-suppress-response-for`: discards the serial responses for an exact normalized command. Use this to simulate a command whose response never arrives.
- `-hold-response-for`: generates but never writes serial responses for an exact normalized command. The mock waits indefinitely, allowing deterministic transport-drop tests by killing the mock process while DDGo is awaiting the response.
- `-probe-omit-result-for`: makes an exact normalized probe command return only `ok`, omitting its probe result as a test hook.
- `-status-position-field`: chooses the primary position spelling in status reports: `M`, `MPos`, `WPos`, or `W`.
- `-status-wco`: enables a WCO status field with an `X,Y,Z` offset triple.
- `-status-fs`: enables an FS status field with a `feed,spindle` pair.

Flag values are validated at startup. `-status-position-field W` cannot be combined with `-status-wco`. The `-status-wco` value must contain three finite comma-separated numbers, and `-status-fs` must contain two finite comma-separated numbers.

## Debug HTTP API

The debug server exposes these endpoints:

```text
GET  /state
GET  /commands
GET  /responses
GET  /events
GET  /profile
POST /reset
POST /hard-limit?axis=X
```

- `/state` returns the current controller snapshot.
- `/commands` returns command log entries.
- `/responses` returns response log entries.
- `/events` returns event log entries.
- `/profile` returns firmware and machine profile details.
- `/reset` injects soft-reset-style firmware output and returns the generated responses in the HTTP response.
- `/hard-limit?axis=X|Y|Z` materializes current motion, enters the alarm state, logs a limit event, queues output for serial delivery, and returns the generated firmware responses in the HTTP response.

Both `/reset` and `/hard-limit` require `POST`; other methods receive `405 Method Not Allowed`. `/hard-limit` rejects a missing axis or any axis other than `X`, `Y`, or `Z` with `400 Bad Request`.

```bash
curl http://127.0.0.1:8088/state
curl http://127.0.0.1:8088/commands
curl -X POST http://127.0.0.1:8088/reset
curl -X POST 'http://127.0.0.1:8088/hard-limit?axis=X'
```

## Serial and pending-output semantics

Normal firmware responses are written to the PTY as commands are processed. Debug-triggered output from `/hard-limit` is instead queued for serial delivery and drains on the next inbound PTY byte, commonly a status poll (`?`). A soft reset, whether triggered through `/reset` or by a serial reset byte, clears pending serial output; the HTTP reset response itself is returned to the HTTP caller rather than queued. An `ok` response means that a command was accepted, not that its motion has completed.

## Motion model

- The mock does not model acceleration or deceleration.
- Moves interpolate linearly over time.
- Jogs enter the `Jog` state.
- Jog cancel materializes the current position and clears active and queued motion.
- Continuous jogs are represented as long target moves.
- A hard limit materializes the current position and enters `Alarm`.
- Unlock clears reset and hard-limit alarm state.

## Status report variants

The default primary position field is `M`. Supported primary position variants are `M`, `MPos`, `WPos`, and `W`. The `W` primary work-position field cannot be combined with `WCO`; optional `WCO` and `FS` fields can otherwise be enabled with flags.

```bash
go run -tags serial ./cmd/mockgrbl -status-position-field MPos
go run -tags serial ./cmd/mockgrbl -status-position-field WPos
go run -tags serial ./cmd/mockgrbl -status-wco 1,2,3 -status-fs 100,0
```

## Testing with mockgrbl

Run the Linux serial/mockgrbl end-to-end suite with:

```bash
go test -tags 'serial mockgrbl_e2e' ./internal/integration/mockgrbl
```

Focused commands for recent behavior groups include:

```bash
go test -tags 'serial mockgrbl_e2e' -run 'HardLimit|Alarm|AckWait|MacroQuery|PendingSerial' ./internal/integration/mockgrbl
go test -tags 'serial mockgrbl_e2e' -run 'ReconnectsAfter.*MockProcessExit|ExplicitDisconnect|TransportDisconnected' ./internal/integration/mockgrbl
```

These tests are Linux-only because they use PTYs. CI runs the mockgrbl integration suite on Ubuntu; macOS and Windows jobs intentionally skip the Linux-only mockgrbl integration steps.

## Common scenarios

1. Inspect live state:

   ```bash
   curl http://127.0.0.1:8088/state
   ```

2. Simulate a hard limit while jogging:

   ```bash
   curl -X POST 'http://127.0.0.1:8088/hard-limit?axis=X'
   ```

3. Delay responses:

   ```bash
   go run -tags serial ./cmd/mockgrbl -response-delay 250ms
   ```

4. Simulate a missing `$G` acknowledgement:

   ```bash
   go run -tags serial ./cmd/mockgrbl -suppress-response-for '$G'
   ```

5. Simulate deterministic process loss while DDGo waits for `$G`:

   ```bash
   go run -tags serial ./cmd/mockgrbl -hold-response-for '$G'
   ```
