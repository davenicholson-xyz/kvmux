# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A software KVM (keyboard/video/mouse) switch in Go. The **server** runs on NixOS (Linux) and reads raw input from `/dev/input` evdev devices. The **client** runs on macOS (or Linux) and injects mouse/keyboard events via robotgo. The two communicate over TCP with a simple binary framing protocol.

## Build

The dev environment is a Nix flake. Enter it with `nix develop` (or direnv if configured). `CGO_ENABLED=1` is required (set automatically by the flake's shellHook).

```bash
go build ./cmd/kvmux-server/   # Linux only (uses evdev, uinput)
go build ./cmd/kvmux-client/   # macOS or Linux
```

There are no tests. `go vet ./...` is the only static check.

## Running

**Server** (Linux, needs root for `/dev/input` access):
```bash
sudo ./server --side right          # client monitor is to the right
sudo ./server --keyboard /dev/input/by-id/...-event-kbd   # explicit device if auto-detect fails
sudo ./server --screen 2560x1440 --scale 1.25             # override auto-detect
```

**Client** (macOS — grant Accessibility permission in System Settings):
```bash
./client --server <ip> --side right   # this machine is to the right of the server
```

## Architecture

### Wire protocol (`internal/proto/proto.go`)
Fixed 3-byte header: `[MsgType u8][payload_len u16 BE][payload]`. All messages go both ways except where noted. Key message types: `MsgMouseEnter/Leave` (control handoff), `MsgMouseDelta` (dx/dy/scroll), `MsgMouseButton`, `MsgKeyEvent`, `MsgHeartbeatPing/Pong`.

### Server (`cmd/kvmux-server/`)
Single-client, synchronous. `main()` opens the mouse and all detected keyboard evdev devices, starts one goroutine per device writing to `evCh` / `kbCh` channels, then calls `handleClient()` which blocks until the client disconnects.

**Edge detection** uses a virtual cursor (`vx`, `vy`) that tracks relative mouse deltas. "Push-through" fires when `vx`/`vy` is already clamped at the configured edge and another delta arrives in the same direction. On trigger: `EVIOCGRAB` both mouse and all keyboard fds (exclusive kernel grab — compositor/X stops seeing events), send `MsgMouseEnter` to client.

**Cursor position sync** uses `xdotool` (found by scanning `/proc/*/environ` for the real user's `DISPLAY`/`XAUTHORITY` when running under sudo) to read the actual cursor position at crossover time, correcting drift between the virtual tracker and reality.

**Wayland/X11 note**: evdev reads bypass the compositor entirely. The grab (`EVIOCGRAB`) is what stops the compositor from seeing events during remote mode.

**Platform files**: `warp_linux.go` contains `warpMouseToCenter` (xdotool first, falls back to uinput corner-slam), `readCursorPos`, and `findDisplayEnv`.

### Client (`cmd/kvmux-client/`)
Receives events from server and injects them locally. Mouse injection is platform-split: `mouse_darwin.go` uses CGo + CoreGraphics (`kvmMoveMouse`) sending `kCGEventLeftMouseDragged` vs `kCGEventMouseMoved` depending on button state. `mouse_linux.go` uses robotgo.

**Return-to-server**: client tracks its own virtual cursor. When it hits the opposite edge from entry and another delta pushes further, it sends `MsgMouseLeave` back to the server. Both sides encode the Y% (for left/right edges) or X% (for top/bottom edges) as a uint16 so the cursor appears at the same relative position on the new screen.

### evdev (`internal/evdev/evdev.go`)
Linux-only (`//go:build linux`). `ReadEvents` accumulates relative events (REL_X, REL_Y, REL_WHEEL, REL_HWHEEL) within an EV_SYN batch, emitting a single `KindMove` event per sync. Mouse buttons emit `KindButton`. **Keyboard keys emit `KindKey` immediately** (not waiting for EV_SYN) so they aren't delayed by the sync gate.

`OpenKeyboards` (plural) opens **all** detected keyboard devices — necessary because a physical keyboard often appears as multiple evdev nodes (main keys + media keys, etc.). All write to the same `kbCh` channel. Auto-detection searches `/dev/input/by-id/*-event-kbd`, then `/dev/input/by-path/*-event-kbd`, then `/proc/bus/input/devices` (EV_KEY + EV_REP + no EV_REL heuristic).

## Known constraints

- **Single client only**: the server's main loop is `handleClient()` — not concurrent. A second connection queues until the first disconnects.
- **Pointer acceleration mismatch**: evdev reports raw hardware counts; the client OS applies its own acceleration. xdotool readback at crossover time corrects the virtual position but inter-crossover drift still accumulates.
- **robotgo on macOS requires Accessibility permission** in System Settings → Privacy & Security → Accessibility.
- **Server needs root** (or `input` group membership) to open `/dev/input` devices and issue `EVIOCGRAB`.
