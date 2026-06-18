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

### `M102` — evaluate an expression and write a WCS axis

```gcode
M102 <destination-WCS-axis> = <expression>
```

`M102` evaluates a finite arithmetic expression and writes the result to the explicit destination WCS axis through the runtime WCS writer. The handler does not verify writeback. Expressions support numeric literals, process-local variables, compact WCS-axis references such as `G54Z`, unary `+`/`-`, `+`, `-`, `*`, `/`, and parentheses. WCS offsets are read through `$#` only when the expression references a WCS value. Inside M102/M106 expressions, WCS-axis references must use compact form such as `G54Z`; spaced forms such as `G54 Z` and `WCS G54 Z` are not expression syntax.

Examples:

```gcode
M102 G54Z = (G55Z + G56Z) / 2
M102 G54X = depth + 0.125
M102 G55Y = -1.25 * 2
```

### `M106` — assert a numeric comparison

```gcode
M106 <left-expression> <op> <right-expression> [ERROR <message>]
```

`M106` evaluates both operands as finite arithmetic expressions and compares them with one of `<`, `<=`, `>`, `>=`, `==`, or `!=`. Equality uses exact numeric comparison. If the assertion is true, the handler succeeds silently. If false, it fails the program with either the custom `ERROR` message or a generated assertion failure. WCS offsets are read through `$#` only when an operand references a WCS value.

Examples:

```gcode
M106 G54Z <= G55Z
M106 G54X == 0
M106 depth > -1.0 ERROR depth is too shallow
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


### `M109` — collect a contour probe point

Syntax:

```gcode
M109 <probe-command>
```

`M109` runs the provided probe command through the macro runtime probe path. When the controller reports a successful probe result such as `[PRB:x,y,z:1]`, DDGo stores the reported machine-coordinate probe point in the contour point store. Duplicate contour points with the same `X` and `Y` coordinates are rejected.

`M109` only collects contour points. It does not enable contour compensation, fit a surface, rewrite motion, or apply Z compensation. Failed probes, no-contact probe results, missing probe results, controller errors, alarms, and missing probe commands fail the macro and do not add a contour point.

Example:

```gcode
M109 G38.2 Z-5 F100
```

### `M110` — enable contour mode

```gcode
M110
```

Enables contour mode after at least three contour points have been collected. This only sets the contour lifecycle flag for future compensation behavior. It does not fit a surface or rewrite motion yet.

### `M111` — disable contour mode

```gcode
M111
```

Disables contour mode without clearing collected contour points. Disabling contour mode is idempotent.

### `M112` — clear contour data

```gcode
M112
```

Clears collected contour points and disables contour mode.

## Runtime capabilities currently available

Registered handlers can use the current runtime to:

- send a controller line and wait for `ok`;
- send a query line and collect intermediate responses until `ok`;
- read WCS offsets through `$#`;
- write WCS offsets through `G10 L2`;
- read current machine and work positions from parsed status reports;
- run probe commands during an active program and read the last successful probe point;
- access process-local variables;
- access contour state, add contour points, and control the contour lifecycle.

## Current limitations

- `M100`, `M101`, `M102`, `M106`, `M107`, `M108`, `M109`, `M110`, `M111`, and `M112` are registered by the default macro engine.
- WCS-axis references currently support documented offset registers `G54` through `G59` and axes `X`, `Y`, and `Z`.
- Variables use the conservative grammar `[A-Za-z_][A-Za-z0-9_]*`.
- Contour surface fitting is not implemented yet.
- Contour motion rewriting is not implemented yet. Enabling contour mode does not affect G-code motion until surface fitting and rewriting are implemented.

## Planned macro implementation order

1. Contour surface fitting.
2. Contour motion rewriting / Z compensation.
