package wire_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	v1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// TestRoundTrip is a smoke test that the generated types marshal and unmarshal.
// Golden vectors for the envelope (HLD section 5) come later; this only proves
// the schemas are usable.
func TestRoundTrip(t *testing.T) {
	in := &v1.Hello{
		ProtoVersion:   1,
		DeviceId:       "abcdefghijklmnop",
		DisplayName:    "desktop-home",
		Platform:       "linux",
		ProtectionTier: v1.ProtectionTier_PROTECTION_TIER_KEYSTORE,
		Capabilities: []*v1.Capability{
			{Id: v1.CapabilityId_CAPABILITY_ID_FILES, Version: 1},
			{Id: v1.CapabilityId_CAPABILITY_ID_CLIPBOARD, Version: 1},
		},
	}
	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out v1.Hello
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(in, &out) {
		t.Fatalf("round trip differs:\n in: %v\nout: %v", in, &out)
	}
	if got := len(out.Capabilities); got != 2 {
		t.Errorf("capabilities = %d, want 2", got)
	}
}

// TestTrustLevelOrdering guards the scale D-20 and D-25 depend on: revocation
// and granting must compare against the same ladder.
func TestTrustLevelOrdering(t *testing.T) {
	if v1.TrustLevel_TRUST_LEVEL_UNPAIRED >= v1.TrustLevel_TRUST_LEVEL_TRUSTED {
		t.Error("unpaired must rank below trusted")
	}
	if v1.TrustLevel_TRUST_LEVEL_TRUSTED >= v1.TrustLevel_TRUST_LEVEL_OWNED {
		t.Error("trusted must rank below owned")
	}
}
