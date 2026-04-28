# CNC UI foundation

This is a starter Go module for a small CNC / GRBL-style desktop UI.

It is intentionally split into a thin UI layer and a testable core:

- `internal/app`: controller and app event model
- `internal/grbl`: GRBL command building and basic status parsing
- `internal/transport`: serial transport interface, fake transport, and real serial transport behind a build tag
- `internal/ports`: serial port discovery helpers behind a build tag
- `internal/ui`: MIQT / Qt Widgets UI behind a build tag
- `cmd/cncui`: application entrypoint

## Layout

The UI follows the requested layout:

- **Left pane**: console with command line + send button underneath
- **Right pane**: connection controls, jog controls, machine action buttons, status labels

The connection pane now contains just:

- port selector
- refresh ports button
- connect / disconnect button

This keeps connection controls focused only on selecting a USB port and connecting.

## Simpler port discovery seam

Port discovery no longer uses a one-method `SystemLister` type. The controller now accepts a plain function of type `ports.ListFunc`, and the real app wires it up with `ports.ListPorts`.

That keeps the seam easy to test while avoiding unnecessary objects.

## Why build tags are used

The real serial implementation and MIQT UI are behind build tags:

- `serial` enables the real USB/TTY serial implementation and port discovery
- `miqt` enables the Qt / MIQT UI

This keeps the core logic testable on machines that do not have Qt installed.

## Run tests

Core and stub-path tests work without Qt installed:

```bash
go test ./...
```

If you want to include the serial-tagged transport and port-listing tests on a machine with the serial dependency downloaded:

```bash
go test -tags serial ./internal/transport ./internal/ports ./internal/app ./internal/grbl
```

## Build the real app

On a machine with Qt 5 development packages installed:

```bash
go build -tags 'miqt serial' ./cmd/cncui
```

## Linux Qt prerequisites

A minimal Debian/Ubuntu setup for MIQT Qt 5 is typically:

```bash
sudo apt install qtbase5-dev build-essential golang-go pkg-config
```

## Notes

- The serial transport uses `go.bug.st/serial`.
- The UI is written directly against `github.com/mappu/miqt/qt`.
- The no-tag build prints an error telling you to rebuild with tags.
- The current implementation sends conservative baseline GRBL commands for jog, unlock, home, hold, resume, status, and soft reset.
- This is a foundation, not a complete production operator console yet.

## Suggested next additions

- configurable machine profile for GRBL-variant differences
- richer status parsing (`MPos`, `WPos`, alarms, feed/spindle overrides)
- per-button enable/disable rules by machine state
- reconnect flow and explicit serial read timeouts
- log export and persistent user settings
- integration tests against your MockGRBL implementation
