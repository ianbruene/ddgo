# Linux serial-port access

DDGo reports all enumerated port metadata when ports appear, disappear, or the
user refreshes the list. The production controller tested on Linux enumerates
as an Arduino Due native USB port with USB VID `2341` and PID `003e`; its serial
number may be empty. A fallback enumeration with `USB=false` and empty IDs is
shown in the diagnostic, but is deliberately not auto-connected because it
cannot positively identify the controller.

## Persistent permissions

Running `chmod` on `/dev/ttyACM0` is only temporary: unplugging the controller
destroys that device node and Linux creates a new one with the system's normal
permissions. Inspect the actual owner and group first:

```sh
ls -l /dev/ttyACM0
```

If the port is owned by a serial-device group such as `dialout`, add your user
to the group shown by that command. On Debian and Ubuntu, for example:

```sh
sudo usermod -aG dialout "$USER"
```

Group names vary between distributions. Log out and back in (or restart the
user session) before testing again so the new membership is active.

An administrator can alternatively test and install a controller-specific udev
rule. For the verified controller IDs, the rule is conceptually:

```udev
SUBSYSTEM=="tty", ATTRS{idVendor}=="2341", ATTRS{idProduct}=="003e", GROUP="dialout", MODE="0660"
```

Confirm the attributes against the board's actual udev hierarchy and replace
`dialout` with the serial-device group used by the system before installing the
rule. Group-based `0660` access is preferred to world-writable `0666` access.
