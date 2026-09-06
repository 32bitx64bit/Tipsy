// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package discord

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	opHandshake = 0
	opFrame     = 1
	opClose     = 2
	opPing      = 3
	opPong      = 4
	ipcVersion  = 1
	maxFrame    = 16 << 10
)

var (
	errNoSocket = errors.New("discord ipc socket not found")
	errFrame    = errors.New("discord ipc frame is invalid")
)

// Client is a Discord IPC connection used only for SET_ACTIVITY.
type Client struct {
	conn  net.Conn
	nonce atomic.Uint64
	pid   int
}

func newClient(conn net.Conn, pid int) *Client {
	if pid <= 0 {
		pid = os.Getpid()
	}
	return &Client{conn: conn, pid: pid}
}

func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *Client) handshake(appID string) error {
	body, err := json.Marshal(struct {
		V        int    `json:"v"`
		ClientID string `json:"client_id"`
	}{V: ipcVersion, ClientID: appID})
	if err != nil {
		return err
	}
	if err := c.write(opHandshake, body); err != nil {
		return err
	}
	op, payload, err := c.read()
	if err != nil {
		return err
	}
	if op == opClose {
		return fmt.Errorf("discord handshake closed")
	}
	if op == opPing {
		_ = c.write(opPong, payload)
		op, _, err = c.read()
		if err != nil {
			return err
		}
	}
	if op != opFrame && op != opHandshake {
		return fmt.Errorf("discord handshake opcode %d", op)
	}
	return nil
}

func (c *Client) setActivity(act *activityPayload) error {
	args := struct {
		PID      int              `json:"pid"`
		Activity *activityPayload `json:"activity"`
	}{PID: c.pid, Activity: act}
	cmd := struct {
		Nonce string `json:"nonce"`
		Cmd   string `json:"cmd"`
		Args  any    `json:"args"`
	}{
		Nonce: strconv.FormatUint(c.nonce.Add(1), 10),
		Cmd:   "SET_ACTIVITY",
		Args:  args,
	}
	body, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	return c.write(opFrame, body)
}

func (c *Client) write(op uint32, payload []byte) error {
	if c == nil || c.conn == nil {
		return errNoSocket
	}
	if len(payload) > maxFrame {
		return errFrame
	}
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:4], op)
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(len(payload)))
	if _, err := c.conn.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := c.conn.Write(payload)
	return err
}

func (c *Client) read() (uint32, []byte, error) {
	if c == nil || c.conn == nil {
		return 0, nil, errNoSocket
	}
	var hdr [8]byte
	if _, err := io.ReadFull(c.conn, hdr[:]); err != nil {
		return 0, nil, err
	}
	op := binary.LittleEndian.Uint32(hdr[0:4])
	n := binary.LittleEndian.Uint32(hdr[4:8])
	if n > maxFrame {
		return 0, nil, errFrame
	}
	body := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(c.conn, body); err != nil {
			return 0, nil, err
		}
	}
	return op, body, nil
}

func (c *Client) setReadDeadline(d time.Duration) {
	if c == nil || c.conn == nil {
		return
	}
	if d <= 0 {
		_ = c.conn.SetReadDeadline(time.Time{})
		return
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(d))
}

// IPCPaths returns candidate Discord IPC sockets for native, Snap, and Flatpak Discord.
func IPCPaths(getenv func(string) string) []string {
	if getenv == nil {
		getenv = os.Getenv
	}
	runtimeDir := strings.TrimSpace(getenv("XDG_RUNTIME_DIR"))
	if runtimeDir == "" {
		runtimeDir = filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	}
	bases := []string{
		runtimeDir,
		filepath.Join(runtimeDir, "app", "com.discordapp.Discord"),
		filepath.Join(runtimeDir, "app", "com.discordapp.DiscordCanary"),
		filepath.Join(runtimeDir, "app", "com.discordapp.DiscordPTB"),
		filepath.Join(runtimeDir, "snap.discord"),
	}
	out := make([]string, 0, len(bases)*10)
	seen := make(map[string]struct{}, len(bases)*10)
	for _, base := range bases {
		for i := 0; i < 10; i++ {
			p := filepath.Join(base, "discord-ipc-"+strconv.Itoa(i))
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}

func dialIPC(paths []string, timeout time.Duration) (net.Conn, string, error) {
	if timeout <= 0 {
		timeout = 200 * time.Millisecond
	}
	for _, p := range paths {
		d := net.Dialer{Timeout: timeout}
		c, err := d.Dial("unix", p)
		if err != nil {
			continue
		}
		return c, p, nil
	}
	return nil, "", errNoSocket
}
