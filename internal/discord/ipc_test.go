// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package discord

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIPCPathsIncludeNativeSnapAndFlatpak(t *testing.T) {
	t.Parallel()
	paths := IPCPaths(func(key string) string {
		if key == "XDG_RUNTIME_DIR" {
			return "/tmp/tipsy-xdg-run"
		}
		return ""
	})
	want := []string{
		"/tmp/tipsy-xdg-run/discord-ipc-0",
		"/tmp/tipsy-xdg-run/app/com.discordapp.Discord/discord-ipc-0",
		"/tmp/tipsy-xdg-run/snap.discord/discord-ipc-0",
	}
	got := stringsJoinSet(paths)
	for _, p := range want {
		if !got[p] {
			t.Fatalf("missing %s in %v", p, paths)
		}
	}
}

type ipcFrame struct {
	Op   uint32
	Body []byte
}

func TestHandshakeAndSetActivityRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "discord-ipc-0")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	got := make(chan ipcFrame, 4)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for i := 0; i < 2; i++ {
			op, body, err := readFrame(conn)
			if err != nil {
				return
			}
			got <- ipcFrame{Op: op, Body: body}
			if i == 0 {
				ready, _ := json.Marshal(map[string]any{"evt": "READY", "cmd": "DISPATCH"})
				_ = writeFrame(conn, opFrame, ready)
			}
		}
	}()

	conn, path, err := dialIPC([]string{sock}, time.Second)
	if err != nil || path != sock {
		t.Fatalf("dial path=%q err=%v", path, err)
	}
	client := newClient(conn, 4242)
	defer client.Close()
	client.setReadDeadline(time.Second)
	if err := client.handshake("1234567890"); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	act := BuildActivity(Place{ID: 1818, Name: "Crossroads"}, true, time.Unix(10, 0))
	if err := client.setActivity(act); err != nil {
		t.Fatalf("set activity: %v", err)
	}

	first := recvFrame(t, got)
	if first.Op != opHandshake {
		t.Fatalf("handshake opcode=%d", first.Op)
	}
	var hs struct {
		V        int    `json:"v"`
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(first.Body, &hs); err != nil || hs.V != 1 || hs.ClientID != "1234567890" {
		t.Fatalf("handshake body=%s err=%v", first.Body, err)
	}
	second := recvFrame(t, got)
	if second.Op != opFrame {
		t.Fatalf("activity opcode=%d", second.Op)
	}
	raw := string(second.Body)
	if !json.Valid(second.Body) || !containsAll(raw, `"SET_ACTIVITY"`, `"Crossroads - Tipsy"`, `"Join"`, `"https://www.roblox.com/games/1818"`, `"pid":4242`) {
		t.Fatalf("activity payload=%s", raw)
	}
	if containsAny(raw, "ticket", "userId", "accessCode", "gameinfo") {
		t.Fatalf("activity leaked secrets: %s", raw)
	}
}

func recvFrame(t *testing.T, ch <-chan ipcFrame) ipcFrame {
	t.Helper()
	select {
	case f := <-ch:
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ipc frame")
		return ipcFrame{}
	}
}

func readFrame(conn net.Conn) (uint32, []byte, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return 0, nil, err
	}
	op := binary.LittleEndian.Uint32(hdr[0:4])
	n := binary.LittleEndian.Uint32(hdr[4:8])
	body := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(conn, body); err != nil {
			return 0, nil, err
		}
	}
	return op, body, nil
}

func writeFrame(conn net.Conn, op uint32, body []byte) error {
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:4], op)
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(len(body)))
	if _, err := conn.Write(hdr[:]); err != nil {
		return err
	}
	_, err := conn.Write(body)
	return err
}

func stringsJoinSet(paths []string) map[string]bool {
	out := make(map[string]bool, len(paths))
	for _, p := range paths {
		out[p] = true
	}
	return out
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

func containsAny(s string, parts ...string) bool {
	for _, p := range parts {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
