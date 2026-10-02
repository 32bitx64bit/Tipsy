// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/mic"
)

const (
	micOffFile = `{"microphone":{"enabled":false}}`
	micOnFile  = `{"microphone":{"enabled":true}}`
)

// isolateMicrophoneConfig gives the test its own empty XDG tree and a clean
// env, and returns the config.json path the door reads. It restores the C-side
// door and watcher so tests cannot leak into each other.
func isolateMicrophoneConfig(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TIPSY_MICROPHONE", "")
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	SetCaptureMuted(false)
	resetMicrophoneFileDoor()
	select { // a wake token left by an earlier test would make the next watcher refresh early
	case <-microphoneDoorWake:
	default:
	}
	t.Cleanup(func() {
		doorFixtureStop()
		stopMicrophoneWatcher(t)
		resetMicrophoneFileDoor()
	})
	return config.Paths().ConfigFile
}

func writeMicrophoneConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := config.AtomicWriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// setMicrophonePoll sets the watcher period for the test and restores the
// default afterwards.
func setMicrophonePoll(t *testing.T, d time.Duration) {
	t.Helper()
	prev := microphoneDoorPollNS.Load()
	microphoneDoorPollNS.Store(int64(d))
	t.Cleanup(func() { microphoneDoorPollNS.Store(prev) })
}

// stopMicrophoneWatcher ends the watcher (it exits once no recorder exists) so
// the next test starts one with its own period.
func stopMicrophoneWatcher(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for microphoneDoorWatching() {
		kickMicrophoneDoorWatcher()
		if time.Now().After(deadline) {
			t.Fatal("microphone door watcher did not stop with no recorder alive")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func waitFor(t *testing.T, limit time.Duration, what string, cond func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for !cond() {
		if time.Since(start) > limit {
			t.Fatalf("timed out after %s waiting for %s; stats=%+v", limit, what, doorFixtureStats())
		}
		time.Sleep(2 * time.Millisecond)
	}
	return time.Since(start)
}

func startDoorFixture(t *testing.T) {
	t.Helper()
	if err := doorFixtureStart(960); err != nil {
		t.Fatal(err)
	}
}

// deliverOne enqueues one buffer and reports whether the client got a
// completion carrying data within the window.
func deliverOne(t *testing.T, window time.Duration) bool {
	t.Helper()
	before := doorFixtureStats()
	if err := doorFixtureEnqueue(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if doorFixtureStats().Callbacks > before.Callbacks {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	after := doorFixtureStats()
	return after.Callbacks > before.Callbacks && after.PCMCallbacks > before.PCMCallbacks
}

// (a) The persisted switch alone closes the hard C gate, with no env set and
// without any JNI query having refreshed it first.
func TestMicrophoneFileOffClosesCaptureGateWithoutEnv(t *testing.T) {
	path := isolateMicrophoneConfig(t)

	// Baseline: no file means defaults, which allow capture.
	if _, callbacks, rc := audioTestCapture(48000, 1, 960); rc != 0 || callbacks != 1 {
		t.Fatalf("default config should allow capture, rc=%d callbacks=%d", rc, callbacks)
	}

	writeMicrophoneConfig(t, path, micOffFile)
	resetMicrophoneFileDoor() // the C cache still says "open"; the recorder must re-read the file itself
	if rc := audioTestCaptureRefused(); rc != 0 {
		t.Fatalf("file enabled=false did not refuse capture, rc=%d", rc)
	}
	if !MicrophoneDisabled() {
		t.Fatal("C gate still open after the recorder saw enabled=false")
	}
	if MicrophoneDoorOpen() {
		t.Fatal("engine-facing door open while the capture gate is closed")
	}
}

// (b)+(d) The toggle is live: file false -> true -> false while one recorder
// runs. Closing shuts the already-open host stream without waiting for client
// input; no data reaches the client while closed; reopening resumes.
func TestMicrophoneFileFlipsLiveAndClosesOpenStream(t *testing.T) {
	path := isolateMicrophoneConfig(t)
	setMicrophonePoll(t, 15*time.Millisecond)
	writeMicrophoneConfig(t, path, micOnFile)
	startDoorFixture(t)

	if !deliverOne(t, 2*time.Second) {
		t.Fatalf("open door did not deliver data: %+v", doorFixtureStats())
	}
	st := doorFixtureStats()
	if !st.StreamOpen || st.Opens != 1 {
		t.Fatalf("expected one open host stream, got %+v", st)
	}

	// Close with the stream open and the client queue empty.
	writeMicrophoneConfig(t, path, micOffFile)
	waitFor(t, 2*time.Second, "host stream closed after the door closed", func() bool {
		return !doorFixtureStats().StreamOpen
	})
	if MicrophoneDoorOpen() {
		t.Fatal("JNI door still open after the file turned it off")
	}

	// While closed: a buffer is not read and nothing is delivered. The queue
	// holds it (the existing denied behaviour) and no stream is reopened.
	if err := doorFixtureEnqueue(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	st = doorFixtureStats()
	if st.Callbacks != 1 || st.PCMCallbacks != 1 || st.Reads != 1 || st.Opens != 1 || st.StreamOpen || st.Queued != 1 {
		t.Fatalf("closed door leaked or reopened: %+v", st)
	}

	// Reopen: the queued buffer completes with host data.
	writeMicrophoneConfig(t, path, micOnFile)
	waitFor(t, 3*time.Second, "capture to resume after the door reopened", func() bool {
		return doorFixtureStats().Callbacks == 2
	})
	st = doorFixtureStats()
	if st.PCMCallbacks != 2 || st.Opens != 2 || !st.StreamOpen {
		t.Fatalf("reopen did not deliver through a fresh stream: %+v", st)
	}
	if !MicrophoneDoorOpen() {
		t.Fatal("JNI door closed after the file turned it back on")
	}

	// And closed again.
	writeMicrophoneConfig(t, path, micOffFile)
	waitFor(t, 2*time.Second, "second close", func() bool { return !doorFixtureStats().StreamOpen })
	if deliverOne(t, 250*time.Millisecond) {
		t.Fatal("data delivered after the second close")
	}
}

// The close bound with the production watcher period: well under a second.
func TestMicrophoneFileCloseBoundWithDefaultPoll(t *testing.T) {
	path := isolateMicrophoneConfig(t)
	if got := time.Duration(microphoneDoorPollNS.Load()); got != 250*time.Millisecond {
		t.Fatalf("default poll = %s, want 250ms", got)
	}
	writeMicrophoneConfig(t, path, micOnFile)
	startDoorFixture(t)
	if !deliverOne(t, 2*time.Second) {
		t.Fatal("no data while the door is open")
	}
	writeMicrophoneConfig(t, path, micOffFile)
	took := waitFor(t, 3*time.Second, "close", func() bool { return !doorFixtureStats().StreamOpen })
	if took > 1500*time.Millisecond {
		t.Fatalf("door took %s to close the open stream", took)
	}
}

// A closed door must not let the watcher or workers call into Go per buffer:
// refreshes happen at create and at stream open only.
func TestMicrophoneNoGoCallPerBuffer(t *testing.T) {
	isolateMicrophoneConfig(t)
	setMicrophonePoll(t, time.Hour)
	startDoorFixture(t)
	if !deliverOne(t, 2*time.Second) {
		t.Fatal("first buffer not delivered")
	}
	base := microphoneDoorRefreshes.Load()
	for i := 0; i < 40; i++ {
		if !deliverOne(t, 2*time.Second) {
			t.Fatalf("buffer %d not delivered", i)
		}
	}
	if got := microphoneDoorRefreshes.Load(); got != base {
		t.Fatalf("refreshed the persisted switch %d times across 40 buffers; want 0", got-base)
	}
}

// The watcher only exists while a recorder does.
func TestMicrophoneWatcherFollowsRecorderLifetime(t *testing.T) {
	isolateMicrophoneConfig(t)
	setMicrophonePoll(t, 10*time.Millisecond)
	if microphoneDoorWatching() {
		stopMicrophoneWatcher(t)
	}
	startDoorFixture(t)
	if !microphoneDoorWatching() {
		t.Fatal("watcher not started for a live recorder")
	}
	doorFixtureStop()
	waitFor(t, 2*time.Second, "watcher to stop with no recorder", func() bool { return !microphoneDoorWatching() })
	// A second recorder restarts it.
	startDoorFixture(t)
	if !microphoneDoorWatching() {
		t.Fatal("watcher not restarted for a new recorder")
	}
}

// A buffer whose read straddles the door closing is withheld, not delivered.
func TestMicrophoneDoorClosingDuringReadWithholdsBuffer(t *testing.T) {
	path := isolateMicrophoneConfig(t)
	setMicrophonePoll(t, time.Hour) // keep the watcher from republishing the file's "open"
	writeMicrophoneConfig(t, path, micOnFile)
	startDoorFixture(t)
	doorFixtureCloseAfterNextRead()
	if err := doorFixtureEnqueue(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, "the straddling read", func() bool { return doorFixtureStats().Reads >= 1 })
	time.Sleep(250 * time.Millisecond)
	st := doorFixtureStats()
	if st.Callbacks != 0 || st.PCMCallbacks != 0 || st.StreamOpen || st.Queued != 1 {
		t.Fatalf("data from a read that straddled the close reached the client: %+v", st)
	}
	// The file still says "open"; a refresh republishes it and the retry delivers.
	if !refreshMicrophoneDoor() {
		t.Fatal("file should allow capture")
	}
	waitFor(t, 3*time.Second, "retry after the door reopened", func() bool { return doorFixtureStats().Callbacks == 1 })
	if st := doorFixtureStats(); st.PCMCallbacks != 1 {
		t.Fatalf("retry did not deliver data: %+v", st)
	}
}

// (c) Env kill-switches and force-on still win over the persisted switch, and
// they are read live.
func TestMicrophoneEnvOverridesFileOnTheCaptureGate(t *testing.T) {
	path := isolateMicrophoneConfig(t)
	setMicrophonePoll(t, 15*time.Millisecond)

	cases := []struct {
		name    string
		file    string
		disable string
		micEnv  string
		want    bool
	}{
		{"file-on", micOnFile, "", "", true},
		{"file-off", micOffFile, "", "", false},
		{"file-on-MICROPHONE-0", micOnFile, "", "0", false},
		{"file-on-MICROPHONE-off-spaced", micOnFile, "", "  OFF ", false},
		{"file-on-DISABLE-1", micOnFile, "1", "", false},
		{"file-off-MICROPHONE-1-env-wins", micOffFile, "", "1", true},
		{"file-off-MICROPHONE-on-env-wins", micOffFile, "", "on", true},
		{"file-off-DISABLE-0-never-forces-on", micOffFile, "0", "", false},
		{"file-on-DISABLE-1-MICROPHONE-1-newer-name-wins", micOnFile, "1", "1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeMicrophoneConfig(t, path, tc.file)
			t.Setenv("TIPSY_DISABLE_MICROPHONE", tc.disable)
			t.Setenv("TIPSY_MICROPHONE", tc.micEnv)
			resetMicrophoneFileDoor()
			startDoorFixture(t)
			defer func() {
				doorFixtureStop()
				stopMicrophoneWatcher(t)
			}()
			got := deliverOne(t, 600*time.Millisecond)
			if got != tc.want {
				t.Fatalf("capture delivered=%v, want %v (stats %+v)", got, tc.want, doorFixtureStats())
			}
			if open := MicrophoneDoorOpen(); open != tc.want {
				t.Fatalf("MicrophoneDoorOpen()=%v disagrees with the capture gate (%v)", open, tc.want)
			}
		})
	}
}

// An env kill-switch set while a recorder runs closes the open stream, and
// clearing it resumes capture (the persisted switch stays on throughout).
func TestMicrophoneEnvKillSwitchIsLive(t *testing.T) {
	path := isolateMicrophoneConfig(t)
	setMicrophonePoll(t, 15*time.Millisecond)
	writeMicrophoneConfig(t, path, micOnFile)
	startDoorFixture(t)
	if !deliverOne(t, 2*time.Second) {
		t.Fatal("open door did not deliver")
	}
	t.Setenv("TIPSY_MICROPHONE", "0")
	if err := doorFixtureEnqueue(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if st := doorFixtureStats(); st.Callbacks != 1 || st.Reads != 1 || st.StreamOpen {
		t.Fatalf("env kill-switch did not close the running recorder: %+v", st)
	}
	t.Setenv("TIPSY_MICROPHONE", "")
	waitFor(t, 3*time.Second, "capture to resume after the kill-switch cleared", func() bool {
		return doorFixtureStats().Callbacks == 2
	})
}

// The C env decision, the C gate, and mic.EffectiveConfig agree for every
// file state and env combination, including whitespace and case variants:
// there is one door, not two implementations that merely look alike.
func TestMicrophoneDoorMatchesMicEffectiveConfig(t *testing.T) {
	path := isolateMicrophoneConfig(t)
	files := map[string]string{
		"missing":   "",
		"on":        micOnFile,
		"off":       micOffFile,
		"no-key":    `{"dataDir":"/x"}`,
		"malformed": `{"microphone":`,
		"mistyped":  `{"microphone":{"enabled":"nope"}}`,
	}
	values := []string{"", "0", "1", "off", "ON", "true", "FALSE", " yes ", "\toff\n", "no", "maybe", "2"}
	for name, body := range files {
		if body == "" {
			_ = os.Remove(path)
		} else {
			writeMicrophoneConfig(t, path, body)
		}
		for _, disable := range values {
			for _, micEnv := range values {
				t.Setenv("TIPSY_DISABLE_MICROPHONE", disable)
				t.Setenv("TIPSY_MICROPHONE", micEnv)
				lookup := func(k string) (string, bool) { return os.LookupEnv(k) }
				var data []byte
				if body != "" {
					data = []byte(body)
				}
				cfg, _ := mic.EffectiveConfig(data, lookup)
				want := cfg.Allowed()
				if got := MicrophoneDoorOpen(); got != want {
					t.Fatalf("file=%s DISABLE=%q MICROPHONE=%q: MicrophoneDoorOpen()=%v, mic.EffectiveConfig=%v",
						name, disable, micEnv, got, want)
				}
				if got := !MicrophoneDisabled(); got != want {
					t.Fatalf("file=%s DISABLE=%q MICROPHONE=%q: C gate open=%v, mic.EffectiveConfig=%v",
						name, disable, micEnv, got, want)
				}
			}
		}
	}
}

// (e) A missing, unreadable, or malformed config means defaults (open), logged
// honestly once per distinct failure, never with file contents.
func TestMicrophoneConfigUnreadableFallsBackToDefaultsWithLog(t *testing.T) {
	path := isolateMicrophoneConfig(t)
	logs := captureAndroidSlog(t, slog.LevelInfo)

	if !MicrophoneDoorOpen() {
		t.Fatal("missing config must default to open")
	}
	if strings.Contains(logs.String(), "unreadable") {
		t.Fatalf("a missing config file is the normal default, not an error: %s", logs.String())
	}

	writeMicrophoneConfig(t, path, `{"microphone":{"enabled":false}`+"\n"+`SECRET-TOKEN-DO-NOT-LOG`)
	if !MicrophoneDoorOpen() {
		t.Fatal("malformed config must default to open")
	}
	if !MicrophoneDoorOpen() {
		t.Fatal("malformed config must default to open (second query)")
	}
	out := logs.String()
	if n := strings.Count(out, "microphone config unreadable"); n != 1 {
		t.Fatalf("malformed config logged %d times, want once: %s", n, out)
	}
	if strings.Contains(out, "SECRET-TOKEN-DO-NOT-LOG") {
		t.Fatalf("config contents leaked into logs: %s", out)
	}

	// An env kill-switch still closes the door when the file is unreadable.
	t.Setenv("TIPSY_MICROPHONE", "0")
	if MicrophoneDoorOpen() {
		t.Fatal("env kill-switch ignored while the config is malformed")
	}
	t.Setenv("TIPSY_MICROPHONE", "")

	// Unreadable (a directory where the file should be): a different failure,
	// so it logs again; recovery re-arms the log.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if !MicrophoneDoorOpen() {
		t.Fatal("unreadable config must default to open")
	}
	if n := strings.Count(logs.String(), "microphone config unreadable"); n != 2 {
		t.Fatalf("unreadable config logged %d total lines, want 2: %s", n, logs.String())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeMicrophoneConfig(t, path, micOffFile)
	if MicrophoneDoorOpen() {
		t.Fatal("repaired config with enabled=false must close the door")
	}
	writeMicrophoneConfig(t, path, `{"microphone":`)
	if !MicrophoneDoorOpen() {
		t.Fatal("re-broken config must default to open")
	}
	if n := strings.Count(logs.String(), "microphone config unreadable"); n != 3 {
		t.Fatalf("a new failure after recovery should log again, total=%d: %s", n, logs.String())
	}
}

// A recorder created while the config is malformed still captures, and one
// created under a readable enabled=false does not (the owner-reported bug).
func TestMicrophoneRecorderUsesDefaultsOnMalformedConfig(t *testing.T) {
	path := isolateMicrophoneConfig(t)
	writeMicrophoneConfig(t, path, `{"microphone":`)
	startDoorFixture(t)
	if !deliverOne(t, 2*time.Second) {
		t.Fatalf("malformed config should leave capture open: %+v", doorFixtureStats())
	}
}

func TestMicrophoneConfigPathIsIsolatedForTests(t *testing.T) {
	path := isolateMicrophoneConfig(t)
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, filepath.Join(home, ".config")) {
		t.Fatalf("test config path %q points at the real user config", path)
	}
	if !strings.HasPrefix(path, os.Getenv("XDG_CONFIG_HOME")) {
		t.Fatalf("config path %q is outside XDG_CONFIG_HOME %q", path, os.Getenv("XDG_CONFIG_HOME"))
	}
}
