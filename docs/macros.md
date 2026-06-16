# DDGo macros

This document separates DDGo's macro framework support from command behavior that has not been implemented yet.

## Macro framework status

DDGo has an application-level macro interception framework. During program execution, the macro engine can intercept leading M-code-style lines when a handler is registered for that leading code. Macro numbers must be followed by whitespace or the end of the line, so prefix-like controller forms such as `M107.1` and `M107depth 1` pass through as normal G-code.

Handlers receive both `RawArgs` and `CleanArgs`:

- `RawArgs` come from the original parsed raw line after the leading code.
- `CleanArgs` come from the sanitized line after comment stripping and whitespace normalization.

Raw arguments are available for future commands where parentheses, semicolons, comments, or original spacing may matter. Unregistered commands pass through to the controller like normal G-code.

## Implemented macro handlers

### `M100` — write midpoint between two WCS axes

Syntax:

```gcode
M100 <source-WCS-axis-a> <source-WCS-axis-b> <destination-WCS-axis>
```

`M100` reads controller WCS offsets through `$#`, resolves the two source WCS-axis values from the same offset snapshot, computes their midpoint, and writes the midpoint to the destination WCS axis through the runtime WCS writer. After the write succeeds, `M100` reads WCS offsets again and verifies that the destination axis matches the intended midpoint within `0.000001`.

Example:

```gcode
M100 G54X G55X G56X
```

### `M101` — compare two WCS axes

Syntax:

```gcode
M101 <WCS-axis-a> <WCS-axis-b> <tolerance>
```

`M101` reads controller WCS offsets through `$#`, resolves both WCS-axis values from the same offset snapshot, and compares the absolute difference with the finite non-negative tolerance. The handler succeeds silently when the values are equal, within tolerance, or exactly at tolerance. If the values differ by more than the tolerance, the handler fails the program.

Example:

```gcode
M101 G54X G55X 0.001
```

### `M107` — store a variable

Syntax:

```gcode
M107 <variable> <number>
M107 <variable> <WCS-axis>
```

`M107` stores a finite numeric value in the process-local variable store. The value can be a numeric literal, such as `M107 depth -1.25`, or a documented WCS-axis reference, such as `M107 depth G54Z`, `M107 depth G54 Z`, or `M107 depth WCS G54 Z`.

When the source is a WCS-axis reference, DDGo reads controller WCS offsets through `$#` and stores the selected `X`, `Y`, or `Z` value.

### `M108` — write a variable to a WCS axis

Syntax:

```gcode
M108 <variable> <WCS-axis>
```

`M108` looks up a process-local variable and writes its value to the requested WCS axis through the runtime WCS writer. The runtime emits the appropriate `G10 L2` command for the controller.

Example:

```gcode
M107 depth -1.25
M108 depth G54Z
```

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

- Only `M100`, `M101`, `M107`, and `M108` are registered by the default macro engine.
- WCS-axis references currently support documented offset registers `G54` through `G59` and axes `X`, `Y`, and `Z`.
- Variables use the conservative grammar `[A-Za-z_][A-Za-z0-9_]*`.
- The probe runtime method currently returns not-available.
- Probe result parsing/capture is not implemented yet.
- Contour point probing is not implemented yet.
- Contour motion rewriting is not implemented yet.

## Planned macro implementation order

1. Additional non-probe WCS, comparison, and expression handlers.
2. Probe execution and probe result capture.
3. Contour point collection.
4. Contour surface fitting and motion rewriting.
