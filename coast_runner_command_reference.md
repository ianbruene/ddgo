# Coast Runner CR-1 Command Reference

## Scope
This document summarizes the commands the **Coast Runner CR-1** is documented to accept, based primarily on the manual’s **Section 9.1 (G-code Commands)** and **Section 9.2 (grbl Commands and Settings)**. It is intended as a concise implementation and interoperability reference.

## Conventions
- `X...`, `Y...`, `Z...` are axis words.
- `F...` is feedrate.
- `S...` is spindle RPM.
- `Pn` in `G10` selects work offset register `P1`–`P6`, corresponding to `G54`–`G59`.
- Commands marked **CRWrite-only** are macros interpreted by CRWrite and are **not** sent directly to grbl as valid G-code.

## 1. Motion and coordinate system commands

### Axis words
`X...` `Y...` `Z...`
Specify motion targets or distances, depending on modal state. Axis words do not define motion behavior on their own; they work together with motion commands such as `G0`, `G1`, `G38.x`, or jogging.

### Motion mode
`G0`
Rapid motion at the machine’s configured rapid rate.

`G1`
Linear motion at feedrate. Requires a defined feedrate, either already modal or supplied in the block with `F...`.

### Positioning mode
`G90`
Absolute positioning.

`G91`
Relative positioning.

### Coordinate system selection
`G53`
Machine-coordinate motion. Non-modal. Used for absolute moves in machine coordinates.

`G54` `G55` `G56` `G57` `G58` `G59`
Select the active work coordinate system. These are modal.

### Feedrate
`F...`
Set feedrate. Interpreted in inches or millimeters according to the current unit mode. Feedrate affects `G1` motion and related feed-controlled motion, but not `G0` rapid moves.

## 2. Jogging commands

### Jog command
`$J=<move> F<feed>`
Issue a jog. Jogging uses grbl’s dedicated jog path rather than ordinary buffered G-code execution.

Documented jog behavior:
- jogs are always feed-controlled,
- the parser state is read but not modified by the jog command,
- jogs that would violate soft limits are ignored rather than causing a normal motion alarm,
- while jogging, grbl enters `Jog` state and does not accept ordinary movement commands.

Examples:
- `$J=G91 Y-20 F100`
- `$J=G90 X-10 F50`
- `$J=G53 Z-20 F300`

### Jog cancel
`!`
Cancels an active jog when jogging is in progress.

`0x85`
Programmatic jog-cancel character. Also cancels an active jog.

## 3. Spindle commands

`M3`
Enable clockwise spindle rotation.

`M4`
Enable counterclockwise spindle rotation. The manual notes this is useful for probing because it spins “backwards” relative to normal cutting.

`M5`
Stop spindle rotation.

`S...`
Set spindle speed in RPM. The manual documents a CR-1 spindle range of **1500–8000 RPM**; values below minimum will not produce the expected low-speed rotation, and values above maximum are capped in effect.

## 4. Probing and work-offset commands

### Probe until contact
`G38.2 <move> F<feed>`
Probe move that stops on probe trip and alarms if no trip occurs by the end of travel.

`G38.3 <move> F<feed>`
Probe move that stops on probe trip and continues without alarm if no trip occurs.

### Probe until clear
`G38.4 <move> F<feed>`
Probe-clear move; expects the probe to start tripped and alarms if it never clears.

`G38.5 <move> F<feed>`
Probe-clear move; expects the probe to start tripped and continues without alarm if it never clears.

### Set work coordinate system
`G10 L2 Pn ...`
Write absolute machine-referenced values into WCS register `Pn`.

`G10 L20 Pn ...`
Write WCS values relative to the current spindle location. This is the command family the manual recommends for normal probing workflows.

## 5. Units and timing

`G20`
Interpret input distances in inches.

`G21`
Interpret input distances in millimeters.

`G4 P...`
Dwell. Pause execution for the specified duration. The manual notes it can be useful as a synchronization delay between EEPROM-writing operations and later expressions that read them.

## 6. CRWrite-only macro commands

These are **not native grbl commands** on the CR-1. They are intercepted by **CRWrite** and expanded or evaluated there. Sending them directly to grbl is documented to produce an error.

### `M100` — Find and store midpoint
Computes the midpoint between two WCS components and writes the result into a target WCS component.

Example:
- `M100 G54X G55X G56X`
  Writes the midpoint of `G54X` and `G55X` into `G56X`.

### `M101` — Check tolerance
Compares three WCS registers on a specified axis against a tolerance and raises an alarm if the allowed variation is exceeded.

Example:
- `M101 G54 G55 G56 X0.1` 

### `M102` — WCS math
Evaluates a math expression and writes the result into a WCS component.

Examples:
- `M102 G54Y ((G55Y + G56Y)/2)`
- `M102 G54X (G54X - ((.25 / 2) * 25.4))` 

### `M106` — WCS comparison
Compares a WCS component against another WCS component or a constant and raises an alarm with a custom message if the comparison is false.

Example:
- `M106 G54X < 0 Error: G54X is too low`

## 7. Less-common supported G-code

### Arc and plane selection
`G2`
Clockwise arc.

`G3`
Counterclockwise arc.

`G17`
XY plane.

`G18`
XZ plane.

`G19`
YZ plane.

### Reference positions
`G28.1`
Store reference position #1.

`G28`
Move to reference position #1.

`G30.1`
Store reference position #2.

`G30`
Move to reference position #2.

### Tool length and coordinate offsets
`G43.1 Z...`
Apply tool length offset.

`G49`
Clear tool length offset.

`G92`
Apply coordinate-system offset.

`G92.1`
Clear `G92` offset.

### Feedrate mode
`G93`
Inverse-time feed mode.

`G94`
Units-per-minute feed mode. Default.

### Miscellaneous
`N...`
Line number.

`T...`
Tool number. Documented as accepted but ignored because the CR-1 has no automatic tool changer.

`M17`
CR-1-specific stepper high-power mode.

`M18`
CR-1-specific stepper disable until next motion command.

## 8. Program-flow and coolant notes

### Program-flow note
The manual documents **two separate `M2` entries** with conflicting descriptions: one as a pause/resume style behavior and one as end-of-program behavior. That inconsistency appears to be in the manual itself, so `M2` should be treated as **documentation-ambiguous** unless verified against firmware behavior.

### Coolant commands
`M7` `M8` `M9`
The CR-1 manual says coolant commands are allowed but ignored, because the CR-1 does not implement coolant control.

## 9. grbl dollar commands

These commands are documented as CR-1 grbl control commands rather than ordinary G-code. They generally apply when grbl is idle.

`$H`
Home all axes.

`$HX` `$HY` `$HZ`
Home a single axis.

`$X`
Unlock after startup or alarm without homing.

`$L`
Autolevel the X table.

`$LS`
Store the current X-table level state as the reference level.

`$J=...`
Jog command.

`$I`
Report version and hardware info.

`$E`
Dump EEPROM contents.

`$G`
Report parser/modal state.

`$C`
Toggle check-code mode.

`$#`
Report offsets.

`$$`
Report stored parameters.

`$N`
Display startup lines.

`$N0=...` `$N1=...`
Set startup lines.

`$n=...`
Write parameter `n`.

`$RST=#`
Clear offsets and restore parser-state defaults.

`$RST=$`
Restore parameters to factory defaults.

`$RST=*`
Full factory restore.

## 10. Real-time commands

These are documented as taking effect immediately, regardless of queued motion.

`|` or `0x18`
Soft reset.

`?`
Status report.

`!`
Feed hold.

`~`
Resume after hold.

`$`
Help summary.

## 11. CR-1-specific behavior differences from base grbl

The manual documents several differences between the CR-1 fork and baseline grbl 1.1g. The most relevant command-surface differences are:
- stepper power has more than simple on/off states,
- `M17` and `M18` alter stepper behavior,
- alarms require manual soft reset,
- both `|` and `0x18` are accepted as soft reset,
- `$L` provides autolevel functionality,
- probing is interrupt-driven,
- coolant commands are accepted but ignored,
- status formatting includes CR-1-specific probe/limit reporting conventions.


Source: Coast Runner Operator Manual v0.91.pdf
