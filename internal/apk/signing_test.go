package apk

import (
	"encoding/binary"
	"testing"
)

func TestParseSigningPairsHugeSizeDoesNotPanic(t *testing.T) {
	t.Parallel()
	// One valid v2 pair, then a pairSize that would overflow int on 64-bit.
	var buf []byte
	val := make([]byte, 4) // id only, empty value
	binary.LittleEndian.PutUint32(val, apkSigIDV2)
	buf = appendU64(buf, uint64(len(val)))
	buf = append(buf, val...)
	buf = appendU64(buf, 1<<63)
	buf = append(buf, 0, 0, 0, 0) // not enough remaining after the huge size

	pairs, ids, err := parseSigningPairs(buf)
	if err == nil {
		t.Fatal("expected error for huge pairSize")
	}
	if len(ids) != 1 || ids[0] != apkSigIDV2 {
		t.Fatalf("expected first v2 pair to be kept, ids=%v pairs=%d", ids, len(pairs))
	}
}

func TestParseSigningPairsTruncatedTailKeepsFirst(t *testing.T) {
	t.Parallel()
	var buf []byte
	val := make([]byte, 4)
	binary.LittleEndian.PutUint32(val, apkSigIDV2)
	buf = appendU64(buf, uint64(len(val)))
	buf = append(buf, val...)
	buf = append(buf, make([]byte, 16)...)

	pairs, ids, err := parseSigningPairs(buf)
	if err == nil {
		t.Fatal("expected truncated tail error")
	}
	if len(ids) != 1 || ids[0] != apkSigIDV2 {
		t.Fatalf("ids=%v pairs=%d", ids, len(pairs))
	}
}
