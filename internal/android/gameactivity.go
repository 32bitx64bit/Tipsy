// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tipsy-linux/tipsy/internal/loader"
	"golang.org/x/sys/unix"
)

// GameActivityWriteCommandSymbol is the public native_app_glue command API.
// Prefer it whenever the authenticated client exports it: the client's own
// glue then owns the android_app layout.
const GameActivityWriteCommandSymbol = "android_app_write_cmd"

// GameActivityCommandStage identifies the fail-closed command-bridge stage.
type GameActivityCommandStage string

const (
	GameActivityCommandHandle     GameActivityCommandStage = "handle"
	GameActivityCommandDescriptor GameActivityCommandStage = "descriptor"
	GameActivityCommandTransport  GameActivityCommandStage = "transport"
	GameActivityCommandWrite      GameActivityCommandStage = "write"
)

// GameActivityCommandError is returned when Tipsy cannot prove that a command
// will reach the GameActivity glue associated with initializeNativeCode.
// Callers can use errors.As without parsing diagnostic text.
type GameActivityCommandError struct {
	Stage GameActivityCommandStage
	ABI   string
	Err   error
}

func (e *GameActivityCommandError) Error() string {
	if e == nil {
		return "GameActivity command bridge unavailable"
	}
	if e.ABI != "" {
		return fmt.Sprintf("GameActivity command bridge %s (%s): %v", e.Stage, e.ABI, e.Err)
	}
	return fmt.Sprintf("GameActivity command bridge %s: %v", e.Stage, e.Err)
}

func (e *GameActivityCommandError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type gameActivityCommandABI struct {
	name           string
	msgreadOffset  uintptr
	msgwriteOffset uintptr
}

// Guarded descriptor for the public GameActivity native_app_glue LP64
// layout, a last resort after the named android_app_write_cmd export. Both
// fields must validate as opposite ends of the same live pipe before any
// byte is written, so a layout change fails closed.
var gameActivityCommandABIs = [...]gameActivityCommandABI{
	{name: "game-activity-native-app-glue-lp64-v1", msgreadOffset: 0x150, msgwriteOffset: 0x154},
}

// GameActivityCommandWriter delivers NativeAppGlueAppCmd bytes without
// exposing android_app fields to Runtime. namedWrite is preferred when
// present; otherwise abi is the validated compatibility descriptor.
type GameActivityCommandWriter struct {
	handle     uintptr
	namedWrite uintptr
	abi        *gameActivityCommandABI
	mod        *loader.Module
}

// NewGameActivityCommandWriter binds a handle returned by
// GameActivity.initializeNativeCode. namedWrite is the authenticated module's
// optional android_app_write_cmd dynsym address; when mod is supplied,
// namedWrite must reside in mod's executable segment and descriptor probes
// reject addresses overlapping mod's executable segments.
func NewGameActivityCommandWriter(handle, namedWrite uintptr, mods ...*loader.Module) (*GameActivityCommandWriter, error) {
	var mod *loader.Module
	if len(mods) > 0 {
		mod = mods[0]
	}
	if handle < 0x10000 {
		return nil, commandError(GameActivityCommandHandle, "", errors.New("invalid initializeNativeCode handle"))
	}
	if mod != nil {
		if !mod.IsOpen() {
			return nil, commandError(GameActivityCommandHandle, "", errors.New("client module is closed"))
		}
		if mod.OverlapsExec(handle, 1) {
			return nil, commandError(GameActivityCommandHandle, "", errors.New("initializeNativeCode handle overlaps executable segment"))
		}
	}
	if namedWrite != 0 {
		if namedWrite < 0x10000 {
			return nil, commandError(GameActivityCommandHandle, "", errors.New("invalid android_app_write_cmd address"))
		}
		if mod != nil && !mod.ContainsExec(namedWrite, 1) {
			return nil, commandError(GameActivityCommandHandle, "", errors.New("android_app_write_cmd outside module executable segment"))
		}
		return &GameActivityCommandWriter{handle: handle, namedWrite: namedWrite, mod: mod}, nil
	}

	var last error
	for i := range gameActivityCommandABIs {
		abi := &gameActivityCommandABIs[i]
		if _, _, err := validateGameActivityCommandPipe(handle, abi, mod); err == nil {
			return &GameActivityCommandWriter{handle: handle, abi: abi, mod: mod}, nil
		} else {
			last = err
		}
	}
	if last == nil {
		return nil, commandError(GameActivityCommandDescriptor, "", errors.New("no supported GameActivity command descriptor"))
	}
	return nil, last
}

// WriteCommand writes exactly one NativeAppGlueAppCmd byte. Descriptor-backed
// writers revalidate both pipe endpoints on every call so a closed, replaced,
// or changed-layout fd cannot receive an unchecked command.
func (w *GameActivityCommandWriter) WriteCommand(cmd byte) error {
	if w == nil || w.handle < 0x10000 {
		return commandError(GameActivityCommandHandle, "", errors.New("writer is not initialized"))
	}
	if w.mod != nil {
		if !w.mod.IsOpen() {
			return commandError(GameActivityCommandHandle, "", errors.New("client module is closed"))
		}
		if w.mod.OverlapsExec(w.handle, 1) {
			return commandError(GameActivityCommandHandle, "", errors.New("initializeNativeCode handle overlaps executable segment"))
		}
	}
	if w.namedWrite != 0 {
		if w.mod != nil && !w.mod.ContainsExec(w.namedWrite, 1) {
			return commandError(GameActivityCommandHandle, "", errors.New("android_app_write_cmd outside module executable segment"))
		}
		loader.CallP8(w.namedWrite, w.handle, uintptr(cmd), 0, 0, 0, 0, 0, 0)
		return nil
	}
	if w.abi == nil {
		return commandError(GameActivityCommandDescriptor, "", errors.New("writer has no compatibility descriptor"))
	}
	_, writeFD, err := validateGameActivityCommandPipe(w.handle, w.abi, w.mod)
	if err != nil {
		return err
	}
	return writeGameActivityCommandByte(writeFD, cmd, w.abi.name)
}

// gameActivityCommandWriteTimeout bounds a single command write. The
// NativeAppGlue command pipe is consumed by the engine's native app thread;
// when that thread is wedged the pipe fills and a blocking write would hang
// the launch loop or closeLifecycle forever. A full, unconsumed pipe must
// return a clean timeout instead.
const gameActivityCommandWriteTimeout = 250 * time.Millisecond

// errGameActivityCommandWriteTimeout is the clean, typed timeout cause so
// callers can errors.Is it without parsing diagnostic text.
var errGameActivityCommandWriteTimeout = errors.New("command pipe write timed out")

// gameActivityCommandWriteMu serializes descriptor-backed command writes so
// the transient O_NONBLOCK flip below is never observed half-applied by a
// concurrent Tipsy writer (which would otherwise restore the wrong flags).
var gameActivityCommandWriteMu sync.Mutex

// writeGameActivityCommandByte writes one command byte with a bounded wait.
// It polls for writability and writes non-blocking, so a pipe that fills (or
// stays full) returns errGameActivityCommandWriteTimeout within the deadline
// rather than blocking. O_NONBLOCK is set only for the duration of this write
// and restored afterward, so the shared command-pipe descriptor is not
// permanently mutated; the engine's read end is a separate open file
// description and is unaffected.
func writeGameActivityCommandByte(writeFD int, cmd byte, abiName string) error {
	gameActivityCommandWriteMu.Lock()
	defer gameActivityCommandWriteMu.Unlock()

	flags, err := unix.FcntlInt(uintptr(writeFD), unix.F_GETFL, 0)
	if err != nil {
		return commandError(GameActivityCommandWrite, abiName, err)
	}
	if flags&unix.O_NONBLOCK == 0 {
		if _, err := unix.FcntlInt(uintptr(writeFD), unix.F_SETFL, flags|unix.O_NONBLOCK); err != nil {
			return commandError(GameActivityCommandWrite, abiName, err)
		}
		defer func() { _, _ = unix.FcntlInt(uintptr(writeFD), unix.F_SETFL, flags) }()
	}

	deadline := time.Now().Add(gameActivityCommandWriteTimeout)
	buf := []byte{cmd}
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return commandError(GameActivityCommandWrite, abiName, errGameActivityCommandWriteTimeout)
		}
		ms := int(remaining / time.Millisecond)
		if ms < 1 {
			ms = 1
		}
		poll := []unix.PollFd{{Fd: int32(writeFD), Events: unix.POLLOUT}}
		n, err := unix.Poll(poll, ms)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return commandError(GameActivityCommandWrite, abiName, err)
		}
		if n == 0 {
			return commandError(GameActivityCommandWrite, abiName, errGameActivityCommandWriteTimeout)
		}
		if poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return commandError(GameActivityCommandWrite, abiName, errors.New("command pipe endpoint is no longer writable"))
		}
		written, err := unix.Write(writeFD, buf)
		if err == unix.EINTR || err == unix.EAGAIN {
			// EAGAIN: another writer took the last slot between poll and
			// write; re-poll within the same deadline.
			continue
		}
		if err != nil {
			return commandError(GameActivityCommandWrite, abiName, err)
		}
		if written != 1 {
			return commandError(GameActivityCommandWrite, abiName, fmt.Errorf("short command write: %d", written))
		}
		return nil
	}
}

func commandError(stage GameActivityCommandStage, abi string, err error) error {
	return &GameActivityCommandError{Stage: stage, ABI: abi, Err: err}
}

func validateGameActivityCommandPipe(handle uintptr, abi *gameActivityCommandABI, mod *loader.Module) (int, int, error) {
	if abi == nil {
		return -1, -1, commandError(GameActivityCommandDescriptor, "", errors.New("nil compatibility descriptor"))
	}
	readFD, err := readGameActivityFD(handle, abi.msgreadOffset, mod)
	if err != nil {
		return -1, -1, commandError(GameActivityCommandDescriptor, abi.name, fmt.Errorf("read command endpoint: %w", err))
	}
	writeFD, err := readGameActivityFD(handle, abi.msgwriteOffset, mod)
	if err != nil {
		return -1, -1, commandError(GameActivityCommandDescriptor, abi.name, fmt.Errorf("write command endpoint: %w", err))
	}
	if readFD == writeFD {
		return -1, -1, commandError(GameActivityCommandTransport, abi.name, errors.New("command endpoints are not distinct"))
	}
	readStat, err := validatePipeEndpoint(readFD, unix.O_RDONLY)
	if err != nil {
		return -1, -1, commandError(GameActivityCommandTransport, abi.name, fmt.Errorf("read endpoint: %w", err))
	}
	writeStat, err := validatePipeEndpoint(writeFD, unix.O_WRONLY)
	if err != nil {
		return -1, -1, commandError(GameActivityCommandTransport, abi.name, fmt.Errorf("write endpoint: %w", err))
	}
	if readStat.Dev != writeStat.Dev || readStat.Ino != writeStat.Ino {
		return -1, -1, commandError(GameActivityCommandTransport, abi.name, errors.New("command endpoints are not the same pipe"))
	}
	return readFD, writeFD, nil
}

func readGameActivityFD(handle, offset uintptr, mod *loader.Module) (int, error) {
	if handle < 0x10000 {
		return -1, errors.New("invalid descriptor base")
	}
	if handle > ^uintptr(0)-offset {
		return -1, errors.New("descriptor address overflow")
	}
	addr := handle + offset
	var raw [4]byte
	if addr < 0x10000 || addr > ^uintptr(0)-uintptr(len(raw)) {
		return -1, errors.New("descriptor address overflow")
	}
	if mod != nil {
		if !mod.IsOpen() {
			return -1, errors.New("client module is closed")
		}
		if mod.OverlapsExec(addr, uintptr(len(raw))) {
			return -1, errors.New("descriptor address overlaps executable segment")
		}
	}
	var pipeFDs [2]int
	if err := unix.Pipe2(pipeFDs[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		return -1, err
	}
	defer func() {
		_ = unix.Close(pipeFDs[0])
		_ = unix.Close(pipeFDs[1])
	}()
	var wn uintptr
	for {
		var errno unix.Errno
		wn, _, errno = unix.Syscall(unix.SYS_WRITE, uintptr(pipeFDs[1]), addr, uintptr(len(raw)))
		if errno == unix.EINTR {
			continue
		}
		if errno != 0 {
			return -1, errno
		}
		break
	}
	if wn != uintptr(len(raw)) {
		return -1, fmt.Errorf("short descriptor probe: %d", wn)
	}
	for {
		n, err := unix.Read(pipeFDs[0], raw[:])
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return -1, err
		}
		if n != len(raw) {
			return -1, fmt.Errorf("short descriptor read: %d", n)
		}
		break
	}
	return int(int32(binary.LittleEndian.Uint32(raw[:]))), nil
}

func validatePipeEndpoint(fd, access int) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if fd < 0 {
		return stat, errors.New("negative descriptor")
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return stat, err
	}
	if flags&unix.O_ACCMODE != access {
		return stat, errors.New("descriptor access mode does not match command endpoint")
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETPIPE_SZ, 0); err != nil {
		return stat, errors.New("descriptor is not a pipe")
	}
	if err := unix.Fstat(fd, &stat); err != nil {
		return stat, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFIFO {
		return stat, errors.New("descriptor is not a FIFO")
	}
	return stat, nil
}
