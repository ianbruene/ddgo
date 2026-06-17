# DDGo architecture

This document summarizes the current code structure for contributors. It describes what exists now and calls out intentionally deferred behavior.

## Controller

`internal/app.Controller` is the application orchestration layer. It owns:

- connection state and the active transport;
- the application event stream;
- the loaded G-code program;
- the active program run, when one is running;
- the macro engine hook;
- the optional motion rewriter hook;
- the process-local variable store;
- contour state;
- last-probe placeholder state.

The controller coordinates these concerns but keeps lower-level details in focused packages: transport I/O is in `internal/transport`, GRBL command and status helpers are in `internal/grbl`, G-code loading/parsing is in `internal/gcode`, and macro framework types live in `internal/macro`.

## Program execution

Current program execution flow:

1. A G-code file is loaded through `internal/gcode`.
2. Each runnable line is preserved as both raw text and sanitized text.
   - Raw text is trimmed input after BOM removal.
   - Sanitized text removes comments and normalizes whitespace.
3. Starting a program creates an active program run and marks the controller state as running.
4. For each parsed program line, the run loop:
   - waits until the run is not paused;
   - dispatches the line to the macro engine if one is configured;
   - if no macro handler handles the line, passes it through the optional motion rewriter;
   - sends the resulting line and waits for a terminal controller response: `ok`, `error`, or `alarm`;
   - updates program progress.
5. The controller marks the program completed after all lines finish, or failed when send, macro, rewrite, query, or controller response handling returns an error.

Normal program sending only waits on terminal controller responses. Intermediate RX lines are ignored by ordinary program sends instead of being buffered as program output.

## Response collection

DDGo has query-scoped response collection for macro/runtime queries:

- Normal program execution does not buffer every RX line.
- When a macro/runtime query is active, the active run temporarily installs a query response channel.
- All RX lines delivered to the active run are also delivered to that query channel until the query completes.
- Query collection returns intermediate lines when the controller eventually responds with `ok`.
- Query collection fails when the controller responds with `error` or `alarm`.
- Only one query collector can be active for a run at a time.
- WCS offset reads use this path by sending `$#` and parsing the collected offset responses.

## Macro framework

The `internal/macro` package provides the application-level macro interception framework plus the current default batch of built-in command handlers. The default controller installs `macro.NewDefaultEngine()`, so registered built-ins are intercepted during normal program execution instead of being sent raw to the controller.

Implemented framework pieces include:

- `macro.Invocation`, which carries the source `gcode.Line`, leading M-code number, `RawArgs`, and `CleanArgs`.
- Raw vs clean argument handling so handlers can choose between original line content and comment-stripped sanitized content.
- `macro.Registry` for registering handlers by leading M-code number.
- `macro.Engine` for parsing a program line and dispatching to a registered handler.
- `macro.Handler` and `macro.HandlerFunc`.
- Typed nil `HandlerFunc` protection.
- `macro.Error`, which wraps handler errors with source line and code context.
- `macro.Runtime`, the controller-facing capability interface exposed to handlers.
- Default handlers for M100, M101, M102, M106, M107, M108, and M109. Command syntax details live in `docs/macros.md`.

Empty registries and custom macro engines remain available for tests and specialized flows through `SetMacroEngine`.

Currently deferred macro behavior:

- Probe-backed macro behavior is not implemented yet.
- Contour motion compensation is not implemented yet.

## Contour state

`macro.ContourState` currently stores contour points and an enabled/disabled flag. It can reject duplicate X/Y points, and enabling contour mode requires at least three points so a future surface can be defined.

Program start disables contour mode without clearing collected points. Program failure also disables contour mode without clearing points. Actual contour surface fitting, motion rewriting, and Z compensation are deferred.
