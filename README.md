# Tipsy

Tipsy is an open-source Linux compatibility runtime for the **official unmodified Roblox Android x86-64 client**. It is not a reimplementation of Roblox, not a full Android emulator, and not a cheat client.

You supply a legitimate Android x86-64 package. Tipsy verifies it, extracts it into XDG directories, and runs it in a native X11 window with enough Android/JNI/GameActivity compatibility for the official UI, login, and public experiences.

Roblox itself is proprietary and is **never** redistributed, patched, or committed here.

## Current capabilities

Tipsy is under active development. The following is user-visible and live on Linux X11 with a current official x86-64 client.

**Play**

- Official login screen on a native X11 window
- Sign-in through Roblox’s own UI (Tipsy never asks for or logs passwords, cookies, or `.ROBLOSECURITY`)
- Session kept across quit and relaunch the way the Android client would
- Join public experiences with rendering, keyboard, mouse, networking, and audio
- Website Play: `roblox-player:`, `roblox://`, and `https://www.roblox.com/games/...` URIs
- Dual desktop launchers: **Tipsy - Play** skips the menu; **Tipsy - Settings** is setup and configuration

**Graphics and window**

- OpenGL ES via EGL on X11
- Vulkan when the host loader, a physical device, and xcb/xlib WSI are present (Auto prefers Vulkan)
- Resize, EWMH fullscreen, DPI-aware placement, and a default-monitor setting
- Frame-rate modes: Auto, Limited (30–240), or Unlimited (experimental uncapped request)
- Independent VSync (off by default)
- High-quality textures by default; optional low-texture mode to save memory

**Input and audio**

- Keyboard, including held-key repeat
- Mouse, including captured relative look (RMB / shift-lock)
- Committed text in login, Home search, and in-experience chat (official `RbxKeyboard` path)
- Game audio through PulseAudio or PipeWire

**Setup and tooling**

- Qt 6 setup wizard and settings window
- Cryptographic APK / signing-identity checks; ARM-only and tampered packages fail closed
- Local APK, APKM, XAPK, APKS, ZIP, or split sets
- Optional automatic download from APKPure as unofficial transport, then the same official-package verification
- CLI inspect, compare, doctor, and launch tools

## Requirements

| Need | Detail |
| --- | --- |
| CPU / OS | Linux **x86_64** |
| Display | Native **X11** (`DISPLAY` set). This is an X11-first project, not a Wayland client. |
| GPU | Mesa (or equivalent) EGL / OpenGL ES. Vulkan is used when Auto can probe a complete host path. |
| Audio | PulseAudio or PipeWire’s Pulse server |
| Roblox | Official Android **x86-64** client (`com.roblox.client` with `lib/x86_64/libroblox.so`). Not included. |

ARM-only packages, Windows Roblox, and Roblox Studio are out of scope.

## Quick start

### AppImage

If you have a Tipsy AppImage, mark it executable and run it. First launch opens Settings when no client is installed. After setup, **Tipsy - Play** starts Roblox directly.

```sh
chmod +x Tipsy-*-x86_64.AppImage
./Tipsy-*-x86_64.AppImage
```

The image does not contain Roblox. Use the in-app setup assistant, then Play.

### From source

```sh
./scripts/bootstrap.sh
go test ./...
go build -o bin/tipsy ./cmd/tipsy
go build -o bin/tipsy-gui ./cmd/tipsy-gui
```

Install launchers and binaries into `~/.local` (override with `PREFIX`):

```sh
./scripts/install-desktop.sh
```

Then run `tipsy-gui` for setup, or `tipsy launch` once a client is installed.

Build-time packages (names vary by distro): Go 1.23+, gcc, pkg-config, Qt 6 Widgets, libX11, libXext, libXrandr, libXtst, EGL, GLESv2, Pango, Cairo, PulseAudio (`libpulse` / `libpulse-simple`), and Vulkan loader headers if you want the Vulkan path.

Debian/Ubuntu-style examples used by CI:

```sh
sudo apt-get install -y gcc pkg-config \
  libx11-dev libxext-dev libxrandr-dev libxtst-dev \
  libegl1-mesa-dev libgles2-mesa-dev \
  libpango1.0-dev libpulse-dev \
  qt6-base-dev
```

## Installing the Roblox client

Tipsy never ships Roblox bytes. Setup only accepts an official x86-64 package it can verify.

1. Open **Tipsy - Settings** (`tipsy-gui`).
2. Run the setup assistant.
3. Choose **automatic download** (APKPure listing, then signature + ABI checks) or **local files** you obtained lawfully (Play-enabled device backup, XAPK, and similar).
4. Wait for extraction into the XDG data directory.

CLI equivalent for a package you already have:

```sh
tipsy setup /path/to/com.roblox.client.xapk
tipsy doctor
```

Installer APKs that are not Roblox (for example a store’s own downloader) are rejected. Extra language/ARM splits in a multi-ABI bundle are dropped; an ARM-only archive is not accepted.

Updating the client does not replace separately stored account data.

## Playing

```sh
tipsy-gui                 # Settings / first-run setup
tipsy launch              # official client on X11
tipsy launch --probe      # load + JNI_OnLoad only (no game loop)
tipsy launch 'roblox-player:1+launchmode:play+...'
```

Clicking Play on roblox.com can start Tipsy when the Play desktop file owns `roblox` / `roblox-player` (the install script registers this). Studio URIs are rejected. Joining a second place while the client is already running is not wired yet.

Sign in inside the official UI. Do not paste cookies into Tipsy.

## Settings

All of these live in **Tipsy - Settings** and in `~/.config/tipsy/client-settings.json`. Renderer, FPS, VSync, and texture quality need a Roblox restart.

| Setting | Behavior |
| --- | --- |
| Renderer | Auto (Vulkan when the full path exists, otherwise OpenGL), OpenGL, or Vulkan |
| Frame rate | Auto, Limited 30–240, Unlimited (not a guaranteed FPS) |
| VSync | Off by default; independent of the FPS cap |
| Low texture mode | Off by default (high quality); on saves memory/VRAM |
| Default monitor | Main monitor, a named output, or follow-mouse placement |

## Command line

```
tipsy doctor              Host overview (OS, X11, GPU, audio, Qt, install)
tipsy doctor --report     Same, secret-redacted and shareable
tipsy inspect <apk...>    Inspect official APKs / splits
tipsy setup <apk...>      Verify and install
tipsy launch [--probe] [uri]
tipsy diagnose [subsystem]
tipsy diagnose-native <lib.so>
tipsy compare-roblox <old> <new>
tipsy report <apk...>
tipsy config              Show or edit XDG config
tipsy logs                Log directory and TIPSY_LOG help
tipsy version
```

Subsystems for `tipsy diagnose`: `x11`, `graphics`, `audio`, `jni`, `loader`, `roblox`, `auth`.

Machine-readable flags: `tipsy inspect --json`, `tipsy report --json`, `tipsy doctor --json`.

## Logs and data

XDG layout (override the usual `XDG_*_HOME` variables):

| Path | Typical location |
| --- | --- |
| Config | `~/.config/tipsy/` (`config.json`, `client-settings.json`) |
| Client install | `~/.local/share/tipsy/` |
| Logs | `~/.local/state/tipsy/` |

```sh
TIPSY_LOG=all TIPSY_LOG_LEVEL=debug tipsy launch
```

`TIPSY_LOG` is a comma-separated category list or `all`. Categories include `apk`, `loader`, `elf`, `android`, `jni`, `gameactivity`, `x11`, `graphics`, `input`, `audio`, `network`, `auth`, `filesystem`, `qt`, and `runtime`.

Diagnostics never print passwords, cookies, tokens, or `.ROBLOSECURITY`.

## What Tipsy is not

- Not a source of Roblox APKs, `.so` files, or assets
- Not a cheat, injector, executor, or anti-cheat bypass
- Not a Windows-Roblox / Wine wrapper
- Not a complete Android OS
- Not Roblox Studio

**Still limited or unverified**

- Microphone capture is not advertised and remains unverified
- Gamepads / controllers are not implemented
- Native Wayland is not the display target
- In-experience join while a session is already running is not wired
- Horizontal mouse wheel is captured but the current Android client only consumes vertical scroll
- `tipsy repair` is not implemented; use `tipsy setup` to re-extract

Performance claims versus other Linux Roblox runtimes are out of scope until they are measured.

## Packaging

`scripts/build-appdir.sh` produces a versioned AppDir and archive with bundled Qt/XCB runtime libraries and license notices. `scripts/build-appimage.sh` wraps that AppDir with a **pinned local** `appimagetool` (never downloaded implicitly). Payloads are guarded: no APK, `libroblox.so`, or Roblox fonts.

Flatpak is planned later and does not replace AppImage.

## License

Tipsy is [GPL-3.0-or-later](LICENSE). See [NOTICE](NOTICE) for third-party notes.

Roblox is copyright Roblox Corporation and is not part of this project. Users must obtain official packages through legitimate channels.
