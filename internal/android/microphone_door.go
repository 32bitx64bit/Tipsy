// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

/*
#include "android_bridge.h"
*/
import "C"

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/mic"
)

// The microphone door has one answer. The env kill-switches are evaluated live
// in C (opensles.c microphone_env_decision, same rules as
// mic.MicrophoneConfig.WithEnv); the persisted Settings switch (config.json
// "microphone.enabled") is read here and published into a C atomic. The OpenSL
// capture gate, the JNI RECORD_AUDIO / hasSystemFeature answers
// (MicrophoneDoorOpen) and MicrophoneDisabled all read that same pair, so the
// engine-facing answer and the hard gate cannot disagree.
//
// The file half is refreshed:
//   - when a recorder is created and before every host capture-stream open
//     (GoAndroid_MicrophoneRecorderCreated / GoAndroid_MicrophoneDoorRefresh,
//     once per stream, never per buffer);
//   - by a watcher goroutine, while at least one recorder exists, every
//     microphoneDoorPollDefault;
//   - on every MicrophoneDoorOpen call (the JNI door queries).
//
// A missing, unreadable, or malformed file means the defaults (door open) and
// is logged once per distinct failure. Nothing here logs PCM, source names, or
// file contents.

const microphoneDoorPollDefault = 250 * time.Millisecond

var (
	// microphoneDoorPollNS is the watcher period; tests shorten or lengthen it.
	microphoneDoorPollNS atomic.Int64

	// microphoneDoorRefreshes counts file refreshes, so tests can prove the
	// audio path does not call Go per buffer.
	microphoneDoorRefreshes atomic.Uint64

	microphoneDoor struct {
		mu       sync.Mutex // serialises read+publish and guards the fields below
		watching bool
		lastErr  string
	}
)

func init() { microphoneDoorPollNS.Store(int64(microphoneDoorPollDefault)) }

// MicrophoneDoorOpen answers the engine-facing microphone questions
// (RECORD_AUDIO checks, the PermissionsProtocol microphone arm, and
// hasSystemFeature(android.hardware.microphone)). It first re-reads the
// persisted switch, so a Settings toggle is visible immediately and any
// closing transition also shuts the host capture stream, then returns exactly
// what the OpenSL capture gate uses: env kill-switches over the persisted
// switch.
func MicrophoneDoorOpen() bool {
	refreshMicrophoneDoor()
	return !MicrophoneDisabled()
}

// refreshMicrophoneDoor re-reads config.json and publishes the persisted switch
// to the C gate. It returns what the file allows (env is not applied here).
func refreshMicrophoneDoor() bool {
	microphoneDoor.mu.Lock()
	defer microphoneDoor.mu.Unlock()
	microphoneDoorRefreshes.Add(1)
	cfg, err := mic.LoadMicrophoneConfigFile(config.Paths().ConfigFile)
	noteMicrophoneConfigErrorLocked(err) // err => cfg is the defaults (allowed)
	allowed := cfg.Allowed()
	v := C.int(0)
	if allowed {
		v = 1
	}
	if C.tipsy_audio_set_microphone_file_door(v) != 0 {
		if allowed {
			logging.Logger(logging.CatAudio).Info("[audio] microphone setting on: capture may open when the client records")
		} else {
			logging.Logger(logging.CatAudio).Info("[audio] microphone setting off: host capture closed")
		}
	}
	return allowed
}

func noteMicrophoneConfigErrorLocked(err error) {
	if err == nil {
		microphoneDoor.lastErr = ""
		return
	}
	msg := logging.Redact(err.Error())
	if msg == microphoneDoor.lastErr {
		return
	}
	microphoneDoor.lastErr = msg
	logging.Logger(logging.CatAudio).Warn(
		"[audio] microphone config unreadable; using defaults (door open unless an env kill-switch closes it)",
		"error", msg)
}

// ensureMicrophoneDoorWatcher starts the live watcher if it is not running.
// It runs only while OpenSL recorders exist, so processes that never record
// (CLI commands, a GUI between launches) poll nothing.
func ensureMicrophoneDoorWatcher() {
	microphoneDoor.mu.Lock()
	if microphoneDoor.watching {
		microphoneDoor.mu.Unlock()
		return
	}
	microphoneDoor.watching = true
	microphoneDoor.mu.Unlock()
	go microphoneDoorWatch()
}

// microphoneDoorWake lets tests end a long poll sleep early; production never
// sends on it.
var microphoneDoorWake = make(chan struct{}, 1)

func kickMicrophoneDoorWatcher() {
	select {
	case microphoneDoorWake <- struct{}{}:
	default:
	}
}

func microphoneDoorWatchInterval() time.Duration {
	if d := time.Duration(microphoneDoorPollNS.Load()); d > 0 {
		return d
	}
	return microphoneDoorPollDefault
}

func microphoneDoorWatch() {
	for {
		timer := time.NewTimer(microphoneDoorWatchInterval())
		select {
		case <-timer.C:
		case <-microphoneDoorWake:
			timer.Stop()
		}
		// The recorder registers in C before it calls
		// GoAndroid_MicrophoneRecorderCreated, which takes this same mutex, so
		// deciding to exit under the lock cannot miss a new recorder: its
		// ensure call either sees the watcher still running or restarts it.
		microphoneDoor.mu.Lock()
		if C.tipsy_audio_capture_recorder_count() == 0 {
			microphoneDoor.watching = false
			microphoneDoor.mu.Unlock()
			return
		}
		microphoneDoor.mu.Unlock()
		refreshMicrophoneDoor()
	}
}

func microphoneDoorWatching() bool {
	microphoneDoor.mu.Lock()
	defer microphoneDoor.mu.Unlock()
	return microphoneDoor.watching
}

// GoAndroid_MicrophoneDoorRefresh is called from the OpenSL recorder worker
// right before it opens a host capture stream (once per open, not per buffer).
//
//export GoAndroid_MicrophoneDoorRefresh
func GoAndroid_MicrophoneDoorRefresh() {
	refreshMicrophoneDoor()
}

// GoAndroid_MicrophoneRecorderCreated is called from the OpenSL engine after a
// recorder object exists: it publishes the persisted switch before the client
// can record and starts the watcher that makes toggles live.
//
//export GoAndroid_MicrophoneRecorderCreated
func GoAndroid_MicrophoneRecorderCreated() {
	refreshMicrophoneDoor()
	ensureMicrophoneDoorWatcher()
}
