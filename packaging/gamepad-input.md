# Gamepad device access per package format (controller Phase 5)

How a Linux gamepad reaches Tipsy, and the one permission step each
package format needs. No new dependencies, no sandbox rebuilds beyond
the Flatpak finish-arg below.

## What Tipsy opens

- Reads: `/dev/input/event*` with `O_RDONLY|O_NONBLOCK`. Never
  `EVIOCGRAB` by default, so Steam, `evtest`, and the desktop keep
  working alongside Tipsy.
- Rumble (Phase 4, behind `TIPSY_GAMEPAD_RUMBLE`): re-opens the same
  node `O_RDWR` to upload `EV_FF` effects. No extra device rule covers
  this; it is the same node with wider open flags.
- Detection: presence of `BTN_SOUTH` (`0x130`) identifies a gamepad.
  Flight sticks without `BTN_SOUTH` are honestly skipped (never a fake
  pad); see `internal/gamepad/quirks.go` (`flight` rows, best-effort).
- Bluetooth changes nothing on this side: the OS pairs, BlueZ exposes
  the same evdev node, Tipsy's inotify watch + 1 s rescan picks up
  connects, disconnects, and reconnects. There is no Tipsy Bluetooth
  stack. See "Bluetooth" below.

## Host permission model (all formats)

Evdev nodes are `root:input` plus a logind ACL for the active local
session (stock systemd `70-uaccess` tags `ID_INPUT_JOYSTICK` devices
with `uaccess`, so a locally logged-in user normally just works).
Tipsy ships **no custom udev rule** by design: the stock logind ACL is
the primary door, the `input` group is the fallback.

Check state (content-free, no input values):

```sh
id -nG                                  # want: input (fallback door)
getfacl /dev/input/event* 2>/dev/null   # want: user:<you>:rw- via logind
tipsy diagnose gamepad                  # per-pad caps/mapping, EACCES list
tipsy doctor                            # actionable issue when EACCES denies nodes
```

If `diagnose gamepad` reports `degraded` with denied nodes:

1. Prefer the session door: log in locally (not bare `ssh`) so logind
   grants the ACL, then relaunch.
2. Fallback: `sudo usermod -aG input "$USER"` **and relogin**
   (group membership applies at next login, not in the current shell).
3. Never run Tipsy as root or `chmod 666 /dev/input/*` to work around
   this; on `EACCES` Tipsy degrades honestly (engine sees zero pads,
   never a phantom Xbox pad).

## Per-format notes

### Flatpak — `--device=all` is required on Flatpak 1.14

The manifest (`packaging/flatpak/io.github.tipsy_linux.Tipsy.yaml`)
grants `--device=all` alongside `--device=dri`. `--device=input` is
Flatpak 1.15.6+; Ubuntu 24.04 and the GitHub `ubuntu-24.04` publish
runner ship 1.14.6, which rejects that token at `build-finish`
(`Unknown device type input, valid types are: dri, all, kvm, shm`)
and ignores it at runtime. `--device=all` is the 1.14-safe grant that
includes `/dev/input`. Pinned by `packaging/flatpak/gamepad_test.go`
(source + rendered manifest; `--device=input` must stay absent).

Existing installs granted before this landed can opt in without
reinstalling:

```sh
flatpak override --user --device=all io.github.tipsy_linux.Tipsy
```

(or toggle *Devices > All* in Flatseal). On Flatpak 1.15.6+ you can
narrow that to `--device=input` instead. The host permission model
above still applies inside the sandbox: the Flatpak user needs the
logind ACL or the `input` group on the host.

### AppImage — no change

Confirmed, pinned by `packaging/appimage/gamepad_test.go`: `AppRun`
adds no sandbox layer (`bwrap`/`unshare`/device remaps), so host
`/dev/input/event*` nodes stay visible as-is. Only the host permission
model above governs. No AppImage rebuild was needed for Phase 5.

### Debian / RPM / Arch — device-group docs only, no payload change

`build-deb.sh`, `build-rpm.sh`, and `build-pacman.sh` ship no udev
file and no extra group: every target distro already ships the
`input` group plus the logind `uaccess` rule, so there is nothing to
stage. The user-facing step is the fallback in "Host permission
model": add the user to `input` and relogin.

| Distro family | Group | Command |
| --- | --- | --- |
| Debian / Ubuntu | `input` | `sudo usermod -aG input "$USER"`, then relogin |
| Fedora / RHEL / openSUSE | `input` | `sudo usermod -aG input "$USER"`, then relogin |
| Arch / EndeavourOS | `input` | `sudo usermod -aG input "$USER"`, then relogin |

`update-pacman-repo.sh` / repository signing are unaffected.

## Bluetooth

Pairing, PINs, and reconnects are OS-level; Tipsy only rescans evdev.

- Pair in the desktop Bluetooth settings or with `bluetoothctl`
  (`scan on`, `pair`, `trust`, `connect`). Most modern pads
  (Xbox Wireless, DualShock 4, DualSense, Switch Pro, 8BitDo,
  Stadia BLE) pair PIN-less (JustWorks); nothing in Tipsy prompts
  for or stores a PIN.
- DualShock 3 needs **one** USB cable pair via the `sixaxis`
  BlueZ plugin first, then it connects over BT like any other pad.
- 8BitDo pads select their protocol with the physical mode switch
  (XInput = Xbox layout, DInput/Switch = spec layout); the switch
  position at pair time decides which evdev layout Tipsy sees.
- After pairing, the pad appears as the same `/dev/input/event*`
  shape as its USB transport (same VID/PID, bus `Bluetooth`);
  the quirk table stamps BT transports distinctly (`*-bt` labels)
  so `diagnose gamepad` shows which transport is live.
- Reconnect: power the pad on; BlueZ reconnects trusted devices
  automatically. Tipsy's hotplug watch fires on the node
  reappearing; the 1 s periodic rescan covers supervisors that
  coalesce inotify bursts.
- Suspend/resume survival is OS-level: on wake, BlueZ re-establishes
  the link (pad may need one button press to wake), udev recreates
  the node (possibly under a different `eventN` number — Tipsy
  tracks stable player ids across renames, lowest path = player 1),
  and Tipsy rescans. If a pad does not come back, re-check
  `tipsy diagnose gamepad`: a missing node is a BlueZ/udev state
  problem, an EACCES node is the permission model above.

## Diagnostics already wired (link, don't duplicate)

- `tipsy diagnose gamepad` (aliases `pad`, `controller`): per-pad
  name/vendor/product/caps/mapping, env state, honest
  `active`/`degraded`/`disabled`.
- `tipsy doctor`: reports EACCES on `/dev/input/event*` as one
  actionable issue (input group / udev rule / logind ACL /
  Flatpak `--device=all`).
- Env parity (`TIPSY_GAMEPAD`, `TIPSY_GAMEPAD_PATH`,
  `TIPSY_GAMEPAD_DEADZONE*`, `TIPSY_GAMEPAD_INVERT_*`,
  `TIPSY_GAMEPAD_RUMBLE`, `TIPSY_GAMEPAD_DEBUG=1`): `tipsy diagnose`
  help / `internal/app/usage.go`; persisted `"gamepad"` section in
  the settings file, missing JSON = defaults.

## Dependency note

Phase 5 adds no dependency and no cgo: `internal/gamepad` imports
stdlib plus the already-required `golang.org/x/sys/unix` only
(`go list -f '{{join .Deps "\n"}}' ./internal/gamepad` shows no new
tree). No `gamecontrollerdb.txt` is vendored; the quirk table is
seeded from public VID/PID/name/bus knowledge (see the table notes
in `internal/gamepad/quirks.go`).
