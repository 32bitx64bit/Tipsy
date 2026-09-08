// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"

	"github.com/tipsy-linux/tipsy/internal/loader"
	"golang.org/x/sys/unix"
)

// GameActivityWriteCommandSymbol is the public native_app_glue command API.
// Prefer this named function whenever the authenticated client exports it: the
// client's own GameActivity glue then owns the android_app layout.
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

// This is a guarded descriptor for the public GameActivity native_app_glue
// LP64 layout used by the current official client. It is a last resort after
// the named android_app_write_cmd export. Both fields are safely read and must
// validate as opposite ends of the same live pipe before any byte is written.
// A future layout change therefore fails closed here; it is never guessed from
// a Roblox version string, hash, BSS address, or text signature.
var gameActivityCommandABIs = [...]gameActivityCommandABI{
	{name: "game-activity-native-app-glue-lp64-v1", msgreadOffset: 0x150, msgwriteOffset: 0x154},
}

// GameActivityCommandWriter delivers NativeAppGlueAppCmd bytes without
// exposing android_app fields to Runtime. namedWrite is preferred when present;
// otherwise abi is the validated public-layout compatibility descriptor.
type GameActivityCommandWriter struct {
	handle     uintptr
	namedWrite uintptr
	abi        *gameActivityCommandABI
}

// NewGameActivityCommandWriter binds a handle returned by
// GameActivity.initializeNativeCode. namedWrite is the authenticated module's
// optional android_app_write_cmd dynsym address.
func NewGameActivityCommandWriter(handle, namedWrite uintptr) (*GameActivityCommandWriter, error) {
	if handle < 0x10000 {
		return nil, commandError(GameActivityCommandHandle, "", errors.New("invalid initializeNativeCode handle"))
	}
	if namedWrite != 0 {
		return &GameActivityCommandWriter{handle: handle, namedWrite: namedWrite}, nil
	}

	var last error
	for i := range gameActivityCommandABIs {
		abi := &gameActivityCommandABIs[i]
		if _, _, err := validateGameActivityCommandPipe(handle, abi); err == nil {
			return &GameActivityCommandWriter{handle: handle, abi: abi}, nil
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
// writers re-read and revalidate both pipe endpoints on every call so a closed,
// replaced, or changed-layout fd cannot receive an unchecked command.
func (w *GameActivityCommandWriter) WriteCommand(cmd byte) error {
	if w == nil || w.handle < 0x10000 {
		return commandError(GameActivityCommandHandle, "", errors.New("writer is not initialized"))
	}
	if w.namedWrite != 0 {
		loader.CallP8(w.namedWrite, w.handle, uintptr(cmd), 0, 0, 0, 0, 0, 0)
		return nil
	}
	if w.abi == nil {
		return commandError(GameActivityCommandDescriptor, "", errors.New("writer has no compatibility descriptor"))
	}
	_, writeFD, err := validateGameActivityCommandPipe(w.handle, w.abi)
	if err != nil {
		return err
	}
	for {
		n, err := unix.Write(writeFD, []byte{cmd})
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return commandError(GameActivityCommandWrite, w.abi.name, err)
		}
		if n != 1 {
			return commandError(GameActivityCommandWrite, w.abi.name, fmt.Errorf("short command write: %d", n))
		}
		return nil
	}
}

func commandError(stage GameActivityCommandStage, abi string, err error) error {
	return &GameActivityCommandError{Stage: stage, ABI: abi, Err: err}
}

func validateGameActivityCommandPipe(handle uintptr, abi *gameActivityCommandABI) (int, int, error) {
	if abi == nil {
		return -1, -1, commandError(GameActivityCommandDescriptor, "", errors.New("nil compatibility descriptor"))
	}
	readFD, err := readGameActivityFD(handle, abi.msgreadOffset)
	if err != nil {
		return -1, -1, commandError(GameActivityCommandDescriptor, abi.name, fmt.Errorf("read command endpoint: %w", err))
	}
	writeFD, err := readGameActivityFD(handle, abi.msgwriteOffset)
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

func readGameActivityFD(handle, offset uintptr) (int, error) {
	if handle > ^uintptr(0)-offset {
		return -1, errors.New("descriptor address overflow")
	}
	var raw [4]byte
	local := unix.Iovec{Base: &raw[0]}
	local.SetLen(len(raw))
	n, err := unix.ProcessVMReadv(os.Getpid(), []unix.Iovec{local}, []unix.RemoteIovec{{Base: handle + offset, Len: len(raw)}}, 0)
	if err != nil || n != len(raw) {
		// Some managed Linux environments deny process_vm_readv even for the
		// calling process. /proc/self/mem provides the same bounded, non-faulting
		// read and returns an error for an unmapped descriptor address.
		memFD, openErr := unix.Open("/proc/self/mem", unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if openErr != nil {
			if err != nil {
				return -1, fmt.Errorf("safe descriptor read unavailable: %w", err)
			}
			return -1, openErr
		}
		defer unix.Close(memFD)
		n, err = unix.Pread(memFD, raw[:], int64(handle+offset))
		if err != nil {
			return -1, err
		}
	}
	if n != len(raw) {
		return -1, fmt.Errorf("short descriptor read: %d", n)
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
