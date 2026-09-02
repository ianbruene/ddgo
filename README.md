# DDGo

DDGo is a Go-based GRBL-style CNC controller/operator UI. The repository is organized around a testable core controller, with optional real serial and Qt UI layers enabled through build tags.

## Current capabilities

- Serial transport abstraction with a fake transport for controller and package tests.
- Optional real USB/TTY serial transport behind the `serial` build tag.
- Optional MIQT/Qt Widgets UI behind the `miqt` build tag.
- Port discovery seam through `ports.ListFunc`, with real port listing behind the `serial` build tag.
- Console send path for manual controller commands.
- Jog and machine action commands, including jog cancel, unlock, home, hold, resume, status, and soft reset helpers.
- Status polling and status parsing for machine state, machine position, work position, feed, and spindle values.
- G-code file loading through `internal/gcode`.
- Program execution with pause, resume, stop, progress tracking, and terminal-response handling.
- Macro interception framework for registered application-level macro handlers from both loaded G-code programs and the interactive command prompt, with default built-in handlers for M100 midpoint write/verify, M101 WCS comparison, M102 expression write, M106 assertions, M107 variable store, M108 variable writeback, M109 contour point collection, and contour lifecycle control.
- Macro runtime query support for collecting query responses during an active program run.
- Macro runtime probe execution and last successful probe point capture.
- Contour point collection and lifecycle control through default macro handlers.
- WCS offset read/write helper support using `$#` and `G10 L2`.
- Process-local variable store and contour state primitives.
- Contour mode lifecycle reset on program start and program failure.
- Local mock GrblDD controller for serial-style integration tests and controller development.

## What is not implemented yet

- The custom macro set is intentionally non-contiguous: M103-M105 are undefined and pass through like other unregistered commands, while M110 is unsupported legacy functionality rejected locally from files and the command prompt. Contour surface fitting, contour motion rewriting, and Z compensation are not active project requirements unless separately reintroduced later.
- Machine profile/configuration is still future work.
- Persistent user settings are still future work.

## Repository layout

- `internal/app`: controller orchestration, state, events, connection control, program runs, and runtime hooks.
- `internal/gcode`: G-code file loading and runnable-line parsing with raw and sanitized text.
- `internal/grbl`: GRBL command construction and status parsing helpers.
- `internal/macro`: macro interception framework, runtime interfaces, WCS helpers, variables, and contour state primitives.
- `internal/transport`: serial transport interface, fake transport, and real/stub serial implementations.
- `internal/ports`: serial port discovery seam and real/stub implementations.
- `internal/ui`: optional MIQT/Qt Widgets UI and no-tag stub.
- `cmd/ddgo`: application entrypoint.
- `cmd/mockgrbl`: local GrblDD-style mock controller with PTY serial emulation and debug HTTP API.
- `docs/architecture.md`: current architecture notes for contributors.
- `docs/macros.md`: macro framework status, runtime capabilities, limitations, and planned order.
- `docs/mockgrbl.md`: mock controller usage, debug API, test hooks, and integration-test notes.
- `docs/linux-serial.md`: persistent Linux permissions and controller enumeration diagnostics.

## Build tags

The real serial implementation and MIQT UI are behind build tags:

- `serial` enables the real USB/TTY serial implementation and port discovery.
- `miqt` enables the Qt / MIQT UI.

This keeps the core logic testable on machines that do not have Qt installed.

## Testing

Core and stub-path tests work without Qt installed:

```bash
go test ./...
```

If you want to include the serial-tagged transport and port-listing tests on a machine with the serial dependency downloaded:

```bash
go test -tags serial ./internal/transport ./internal/ports ./internal/app ./internal/grbl
```

Linux mockgrbl integration tests use PTYs and require both `serial` and `mockgrbl_e2e` tags:

```bash
go test -tags 'serial mockgrbl_e2e' ./internal/integration/mockgrbl
```

For mock controller usage and debug endpoints, see [`docs/mockgrbl.md`](docs/mockgrbl.md).

## Build/run

On a machine with Qt 5 development packages installed, build the real app with:

```bash
go build -tags 'miqt serial' ./cmd/ddgo
```

A minimal Debian/Ubuntu setup for MIQT Qt 5 is typically:

```bash
sudo apt install qtbase5-dev build-essential golang-go pkg-config
```

Notes:

- The serial transport uses `go.bug.st/serial`.
- The UI is written directly against `github.com/mappu/miqt/qt`.
- The no-tag build prints an error telling you to rebuild with tags.

## Development status

Current roadmap:

- Preserve the behavior of each currently supported macro explicitly identified by the corrected command list. M103-M105 are intentionally undefined. M110 is unsupported legacy functionality and does not need to be completed or preserved as a working feature.
- Add configurable machine profile support.
- Improve UI affordances for program and macro state.
- Add persistence/settings as needed.
