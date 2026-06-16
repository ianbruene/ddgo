# DDGo macros

This document separates DDGo's macro framework support from command behavior that has not been implemented yet.

## Macro framework status

DDGo has an application-level macro interception framework. During program execution, the macro engine can intercept leading M-code-style lines when a handler is registered for that leading code.

Handlers receive both `RawArgs` and `CleanArgs`:

- `RawArgs` come from the original parsed raw line after the leading code.
- `CleanArgs` come from the sanitized line after comment stripping and whitespace normalization.

Raw arguments are available for future commands where parentheses, semicolons, comments, or original spacing may matter. Unregistered commands pass through to the controller like normal G-code.

## Runtime capabilities currently available

Registered handlers can use the current runtime to:

- send a controller line and wait for `ok`;
- send a query line and collect intermediate responses until `ok`;
- read WCS offsets through `$#`;
- write WCS offsets through `G10 L2`;
- read current machine and work positions from parsed status reports;
- access process-local variables;
- access contour state.

## Current limitations

- Built-in macro command handlers are not implemented yet.
- The probe runtime method currently returns not-available.
- Probe result parsing/capture is not implemented yet.
- Contour point probing is not implemented yet.
- Contour motion rewriting is not implemented yet.

## Planned macro implementation order

1. Non-probe WCS, variable, comparison, and expression handlers.
2. Probe execution and probe result capture.
3. Contour point collection.
4. Contour surface fitting and motion rewriting.
