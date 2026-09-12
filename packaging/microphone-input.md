# Microphone access per package format (voice-chat Phase 6)

How host capture reaches Tipsy, and the one permission step each
package format needs. No new dependencies. The Flatpak already grants
`--socket=pulseaudio` (the same socket playback uses); there is no
separate microphone portal today — the xdg-desktop-portal Audio
portal is still a proposal.

## What Tipsy opens

- Capture: OpenSL ES recorder → Pulse/`pipewire-pulse` record stream
  named `"Roblox microphone"`, lazy-opened only when the official
  client starts recording. Playback is unchanged.
- Optional pin: `TIPSY_MICROPHONE_SOURCE` / config `"source"` selects
  a Pulse source by name (unset = host default). Diagnose and logs
  never print that name.
- Door: `internal/mic` `"microphone": { "enabled": true }` plus
  `TIPSY_MICROPHONE` / deprecated `TIPSY_DISABLE_MICROPHONE`. Enabled
  means *allowed*, not always-open.

## Host permission model (all formats)

Pulse/PipeWire session permissions are the door, not a Tipsy udev
rule. Mute and device selection stay in the desktop:

```sh
pavucontrol           # Recording: unmute "Roblox microphone" / default source
wpctl status          # PipeWire: Sources section
tipsy diagnose audio  # door + source count, never PCM, never source names
tipsy doctor          # actionable issue when the door is closed or no source exists
```

If `diagnose audio` reports no capture source, or `doctor` lists
`no capture source found`:

1. Unmute the default source in `pavucontrol` or `wpctl set-mute @DEFAULT_AUDIO_SOURCE@ 0`.
2. Confirm a source exists (`pactl list sources short`); monitors of
   sinks are not capture devices.
3. Flatpak: Pulse must be visible (manifest already has
   `--socket=pulseaudio`; Flatseal → Socket → PulseAudio).
4. Never run Tipsy as root to work around a mute or missing source.

`android.hardware.microphone` already follows the OpenSL env door (§269);
this CLI slice does not re-probe JNI. Doctor does **not** fail for the
file-vs-env leftover (`mic.Allowed()` is canonical; JNI should call it).

## Per-format notes

### Flatpak — `--socket=pulseaudio` already covers capture

Pinned by `packaging/flatpak/microphone_test.go` (source + rendered
manifest). No yaml change. Existing installs already have the socket
from playback. If an override removed it:

```sh
flatpak override --user --socket=pulseaudio io.github.tipsy_linux.Tipsy
```

### AppImage / Debian / RPM / Arch — no payload change

These formats use the host Pulse/PipeWire socket the same way
playback does. No extra package file, group, or udev rule. The
user-facing step is the host permission model above.

## Diagnostics already wired (link, don't duplicate)

- `tipsy diagnose audio`: playback (user-confirmed), mic door + which
  control set it, host Pulse/PipeWire presence, capture-source count,
  pinned-source boolean. Never PCM. OpenSL last-capture counters are
  not exported to Go this slice.
- `tipsy doctor`: "mic disabled" (env or file) and "no capture source
  found" as actionable issues. JNI feature-false is not an issue.
- Env parity (`TIPSY_MICROPHONE`, `TIPSY_DISABLE_MICROPHONE` alias,
  `TIPSY_MICROPHONE_SOURCE`): `tipsy diagnose --help` /
  `internal/app/usage.go`; persisted `"microphone"` section in the
  settings file, missing JSON = defaults.

## Privacy

No PCM, transcripts, cookies, tokens, or `.ROBLOSECURITY` in
diagnose/doctor output. Pulse source names are not printed (count +
"default" / "pinned source set" only).
