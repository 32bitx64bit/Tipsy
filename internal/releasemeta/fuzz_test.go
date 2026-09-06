package releasemeta

import (
	"encoding/json"
	"testing"
)

func FuzzReleaseTargetDecoder(f *testing.F) {
	f.Add([]byte(`{"schema":"tipsy.release-target.v1"}`), "stable/linux/x86_64/a.AppImage", "stable")
	f.Add([]byte(`null`), "../bad", "beta")
	f.Fuzz(func(t *testing.T, data []byte, targetPath, channel string) {
		if len(data) > 128<<10 || len(targetPath) > 512 || len(channel) > 32 {
			return
		}
		raw := json.RawMessage(data)
		_, _ = decodeReleaseTarget(&raw, targetPath, channel)
	})
}

func FuzzMetadataDepth(f *testing.F) {
	f.Add([]byte(`{"signed":{},"signatures":[]}`))
	f.Add([]byte("[[[[[]]]]]"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxRootBytes {
			return
		}
		_ = checkJSONDepth(data, MaxMetadataDepth)
	})
}
