// Package identity owns device keys, the trust store, and the unlock session.
//
// Two Ed25519 keypairs per device (D-20): an identity key that terminates TLS
// and is never gated, keeping the device reachable and running the continuity
// capabilities; and a privilege key, encrypted at rest (D-19) and unsealed only
// by a biometric or passphrase challenge, required for Owned-level operations.
//
// The unlock is scoped per peer and lasts six hours (D-18, D-30). Note that as
// specified this is enforced by policy rather than cryptography: the decrypted
// privilege key can sign for any peer while it is in memory. D-30 records the
// ephemeral-delegation refinement that would close that, which is not adopted.
//
// Protection tiers (D-21) determine what a device is allowed to be: keystore or
// TPM, else a passphrase through Argon2id, else Trusted only with no privilege
// key at all. The trust store record is enumerated in D-30.
//
// Sharp edge: a decrypted private key in Go memory needs deliberate handling --
// locked pages, no core dumps, zeroed on expiry. The runtime copies allocations
// freely, and "held for six hours" is the entire security boundary.
package identity
