// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import (
	"bytes"
	"strconv"
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
