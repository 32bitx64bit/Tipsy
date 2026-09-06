// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	playerLogPoll      = 250 * time.Millisecond
	playerLogMaxLine   = 4096
	playerLogReadChunk = 64 << 10
)

var (
	onGameLoadedColon = []byte("onGameLoaded: placeId:")
	onGameLoadedEq    = []byte("onGameLoaded: placeId = ")
)

// parseOnGameLoadedPlaceID extracts the numeric place id from a named
// DataModel/NativeDM FLog line. It never returns the rest of the line
// (no tickets, cookies, user ids, job ids, or addresses).
func parseOnGameLoadedPlaceID(line []byte) (int64, bool) {
	if id, ok := parsePrefixedInt64(line, onGameLoadedColon); ok {
		return id, true
	}
	return parsePrefixedInt64(line, onGameLoadedEq)
}

func parsePrefixedInt64(line, prefix []byte) (int64, bool) {
	i := bytes.Index(line, prefix)
	if i < 0 {
		return 0, false
	}
	rest := line[i+len(prefix):]
	n := 0
	for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
		n++
	}
	if n == 0 || n > 19 {
		return 0, false
	}
	id, err := strconv.ParseInt(string(rest[:n]), 10, 64)
	if err != nil || id < 0 {
		return 0, false
	}
	return id, true
}

func watchPlayerLogs(ctx context.Context, dir string, poll time.Duration, observe func(int64)) {
	if dir == "" || observe == nil {
		return
	}
	if poll <= 0 {
		poll = playerLogPoll
	}
	if ctx == nil {
		ctx = context.Background()
	}
	baseline := playerLogSizes(dir)
	tick := time.NewTicker(poll)
	defer tick.Stop()

	var (
		name    string
		off     int64
		partial []byte
	)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			cur := newestPlayerLog(dir)
			if cur == "" {
				continue
			}
			if cur != name {
				name = cur
				partial = nil
				if size, ok := baseline[cur]; ok {
					off = size
				} else {
					off = 0
				}
			}
			off, partial = consumePlayerLog(name, off, partial, observe)
		}
	}
}

func playerLogSizes(dir string) map[string]int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	sizes := make(map[string]int64)
	for _, e := range entries {
		if e.IsDir() || !isPlayerLogName(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		sizes[filepath.Join(dir, e.Name())] = info.Size()
	}
	return sizes
}

func newestPlayerLog(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var (
		best    string
		bestMod time.Time
	)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !isPlayerLogName(name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		mod := info.ModTime()
		path := filepath.Join(dir, name)
		if best == "" || mod.After(bestMod) || (mod.Equal(bestMod) && path > best) {
			best = path
			bestMod = mod
		}
	}
	return best
}

func isPlayerLogName(name string) bool {
	if !strings.HasSuffix(name, ".log") || !strings.Contains(name, "_Player_") {
		return false
	}
	return !strings.Contains(name, "_Studio_")
}

func consumePlayerLog(path string, off int64, partial []byte, observe func(int64)) (int64, []byte) {
	fi, err := os.Stat(path)
	if err != nil {
		return off, partial
	}
	if fi.Size() < off {
		off = 0
		partial = nil
	}
	if fi.Size() == off && len(partial) == 0 {
		return off, partial
	}
	f, err := os.Open(path)
	if err != nil {
		return off, partial
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return off, partial
	}
	buf := make([]byte, playerLogReadChunk)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			off += int64(n)
			partial = appendPlayerLogBytes(partial, buf[:n], observe)
		}
		if err != nil {
			break
		}
	}
	return off, partial
}

func appendPlayerLogBytes(partial, chunk []byte, observe func(int64)) []byte {
	data := chunk
	if len(partial) > 0 {
		data = append(append([]byte{}, partial...), chunk...)
	}
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		line := data[:i]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		notePlayerLogLine(line, observe)
		data = data[i+1:]
	}
	if len(data) > playerLogMaxLine {
		return nil
	}
	if len(data) == 0 {
		return nil
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out
}

func notePlayerLogLine(line []byte, observe func(int64)) {
	if len(line) > playerLogMaxLine || observe == nil {
		return
	}
	id, ok := parseOnGameLoadedPlaceID(line)
	if !ok {
		return
	}
	observe(id)
}
