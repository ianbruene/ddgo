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
- last successful probe point state.

The controller coordinates these concerns but keeps lower-level details in focused packages: transport I/O is in `internal/transport`, GRBL command and status helpers are in `internal/grbl`, G-code loading/parsing is in `internal/gcode`, and macro framework types live in `internal/macro`.

The controller owns status-poll cancellation and waits for the polling goroutine to stop during either explicit or unexpected disconnect cleanup. The serial transport owns its read loop and line buffer; partial serial lines are scoped to one connection and are discarded before a replacement read loop starts.

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

DDGo has query-scoped response collection for macro/runtime queries owned by a response session. Program runs own a program execution session, and an interactive macro owns one short-lived interactive command session:

- At most one response-owning command session may exist: a program run, an interactive macro, or neither. Program start is rejected while an interactive macro is active, and interactive macros/manual controls are rejected while a program is active.
- Manual command-prompt submissions and manual transport actions that could produce controller responses are rejected while an interactive macro owns responses. Automatic status polling remains allowed because it uses suppressed requests and status parsing that cannot complete an application command.
- Explicit and unexpected disconnects synchronously terminate an active interactive macro session with the transport-disconnected sentinel, clear ownership, and wake response waiters without failing an idle loaded program or clearing variables/contour points.
- Soft Reset is a realtime command and does not expect a terminal `ok`, but unlike ordinary realtime controls it invalidates outstanding controller command state. Before writing Ctrl-X, DDGo cancels an interactive-macro or manual-line response owner; an interrupted interactive command wakes with `ErrControllerReset`. Generic Soft Reset is rejected while a program is actively executing because program shutdown remains exclusively owned by `StopProgram` and its established Hold/Soft Reset lifecycle.
- Normal program execution does not buffer every RX line.
- When a macro/runtime query is active, the active run temporarily installs a query response channel.
- All RX lines delivered to the active run are also delivered to that query channel until the query completes.
- Query collection returns intermediate lines when the controller eventually responds with `ok`.
- Query collection fails when the controller responds with `error` or `alarm`.
- Only one query collector can be active for a run at a time.
- WCS offset reads use this path by sending `$#` and parsing the collected offset responses.

## Macro framework

The `internal/macro` package provides the application-level macro interception framework plus the current default batch of built-in command handlers. The default controller installs `macro.NewDefaultEngine()`, so registered built-ins are intercepted during both normal program execution and interactive command-prompt submission instead of being sent raw to firmware.

Implemented framework pieces include:

- `macro.Invocation`, which carries the source `gcode.Line`, leading M-code number, `RawArgs`, and `CleanArgs`.
- Raw vs clean argument handling so handlers can choose between original line content and comment-stripped sanitized content.
- `macro.Registry` for registering handlers by leading M-code number.
- `macro.Engine` for parsing a program line and dispatching to a registered handler.
- `macro.Handler` and `macro.HandlerFunc`.
- Typed nil `HandlerFunc` protection.
- `macro.Error`, which wraps handler errors with source line and code context.
- `macro.Runtime`, the controller-facing capability interface exposed to handlers.
- Default handlers for exactly M100, M101, M102, M106, M107, M108, M109, M111, and M112. M103-M105 are intentionally undefined, and M110 is unsupported legacy functionality. Command syntax details live in `docs/macros.md`.

Empty registries and custom macro engines remain available for tests and specialized flows through `SetMacroEngine`.

Current macro behavior includes probe runtime support, M109 contour point collection, and M111/M112 contour lifecycle control. M110 is rejected as unsupported legacy functionality before it can reach the controller from either source. M103-M105 remain intentionally undefined and follow the same pass-through policy as other unregistered commands.

Currently deferred macro behavior:

- Contour surface fitting is not an active project requirement unless separately reintroduced later.
- Contour motion rewriting / Z compensation is not an active project requirement unless separately reintroduced later.

## Contour state

`macro.ContourState` stores contour points and an enabled/disabled lifecycle flag. It rejects duplicate X/Y points. Clearing contour state removes all collected points and disables contour mode.

Program start disables contour mode without clearing collected points. Program failure also disables contour mode without clearing points. Actual contour surface fitting, motion rewriting, and Z compensation are not part of the active project requirements.

## Mock controller

`cmd/mockgrbl` and `internal/mockgrbl` provide a GrblDD-style mock controller for Linux PTY-based integration tests. The mock is intentionally scoped to DDGo development needs: command acceptance, status reports, motion-state simulation, selected `$` queries, probe/WCS helpers, reset/hard-limit behavior, and transport-loss test hooks. Usage details live in [`docs/mockgrbl.md`](mockgrbl.md).
