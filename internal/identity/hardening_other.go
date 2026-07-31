//go:build !unix

package identity

// DisableCoreDumps is a no-op on Windows.
//
// Crash dumps there are produced by Windows Error Reporting rather than by the
// kernel writing a core file, and a process cannot switch that off for itself
// in a way that survives policy. The available mitigation is
// WerRegisterExcludedMemoryBlock, which excludes a specific allocation from
// dumps and needs the locked key's address plumbed to it; that is worth doing
// and is not done here, so the Windows threat model carries this gap
// explicitly (D-57).
func DisableCoreDumps() error { return nil }
