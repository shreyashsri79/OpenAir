package session

import (
	"github.com/shreyashsri79/openair/internal/identity"
	v1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// Wire values are not generated-enum values (D-34). Every conversion between
// the two lives here so that a cast where a conversion belongs is a diff in one
// file rather than a silent protocol bug scattered across the package.
//
// The offset is not uniform, which is the trap:
//
//   - CapabilityId IS offset by one: wire 0 (control) is enum 1, because
//     PROTOCOL.md Appendix B numbers from 0 and proto3 reserves 0.
//   - TrustLevel IS offset by one against identity.TrustLevel, same reason.
//   - ProtectionTier is NOT offset. PROTOCOL.md §7.3 already numbers the tiers
//     1/2/3, so the schema matches the document -- but identity.ProtectionTier
//     numbers them 0/1/2 in the opposite order (TierNone first), so this pair
//     needs a real table, not arithmetic.
//   - Per-capability msgType enums are NOT offset either. PROTOCOL.md never
//     enumerated msgType at all (D-34 defect 1); the schemas are the original
//     definition, so the enum value is the wire value, and 0/UNSPECIFIED is
//     simply not a valid msgType.

// MsgType converts a generated message-type enum to the wire msgType. It is a
// direct numeric conversion, deliberately routed through a named function so
// call sites read as conversions and stay greppable if that ever stops being
// true.
func MsgType[T ~int32](t T) uint16 { return uint16(t) }

// CapIDToWire converts a generated CapabilityId to the envelope capID byte.
// Reports false for UNSPECIFIED, which has no wire representation.
func CapIDToWire(id v1.CapabilityId) (byte, bool) {
	if id == v1.CapabilityId_CAPABILITY_ID_UNSPECIFIED {
		return 0, false
	}
	if _, known := v1.CapabilityId_name[int32(id)]; !known {
		return 0, false
	}
	return byte(int32(id) - 1), true
}

// CapIDFromWire converts an envelope capID byte to the generated CapabilityId.
// Reports false for capIDs this build does not know, which per §3.1 is a
// message to ignore rather than an error.
func CapIDFromWire(capID byte) (v1.CapabilityId, bool) {
	id := v1.CapabilityId(int32(capID) + 1)
	if _, known := v1.CapabilityId_name[int32(id)]; !known {
		return v1.CapabilityId_CAPABILITY_ID_UNSPECIFIED, false
	}
	if id == v1.CapabilityId_CAPABILITY_ID_UNSPECIFIED {
		return id, false
	}
	return id, true
}

// ProtectionTierToWire maps the domain tier onto the wire enum. The two run in
// opposite directions; see the note at the top of this file.
func ProtectionTierToWire(t identity.ProtectionTier) v1.ProtectionTier {
	switch t {
	case identity.TierKeystore:
		return v1.ProtectionTier_PROTECTION_TIER_KEYSTORE
	case identity.TierPassphrase:
		return v1.ProtectionTier_PROTECTION_TIER_PASSPHRASE
	case identity.TierNone:
		return v1.ProtectionTier_PROTECTION_TIER_NONE
	default:
		return v1.ProtectionTier_PROTECTION_TIER_UNSPECIFIED
	}
}

// ProtectionTierFromWire maps the wire enum onto the domain tier. An
// unrecognised or unspecified value degrades to TierNone, which is the safe
// direction: a peer whose tier we cannot read must never be granted Owned
// (§7.3, D-21).
func ProtectionTierFromWire(t v1.ProtectionTier) identity.ProtectionTier {
	switch t {
	case v1.ProtectionTier_PROTECTION_TIER_KEYSTORE:
		return identity.TierKeystore
	case v1.ProtectionTier_PROTECTION_TIER_PASSPHRASE:
		return identity.TierPassphrase
	default:
		return identity.TierNone
	}
}

// TrustLevelToWire converts the domain trust ladder to the wire one.
func TrustLevelToWire(l identity.TrustLevel) v1.TrustLevel {
	switch l {
	case identity.LevelUnpaired:
		return v1.TrustLevel_TRUST_LEVEL_UNPAIRED
	case identity.LevelTrusted:
		return v1.TrustLevel_TRUST_LEVEL_TRUSTED
	case identity.LevelOwned:
		return v1.TrustLevel_TRUST_LEVEL_OWNED
	default:
		return v1.TrustLevel_TRUST_LEVEL_UNSPECIFIED
	}
}

// TrustLevelFromWire converts the wire trust ladder to the domain one. Anything
// unrecognised degrades to LevelUnpaired.
func TrustLevelFromWire(l v1.TrustLevel) identity.TrustLevel {
	switch l {
	case v1.TrustLevel_TRUST_LEVEL_TRUSTED:
		return identity.LevelTrusted
	case v1.TrustLevel_TRUST_LEVEL_OWNED:
		return identity.LevelOwned
	default:
		return identity.LevelUnpaired
	}
}
