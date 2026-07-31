// Package congestion installs OpenAir's congestion controller on a QUIC
// connection.
//
// OpenAir runs BBR rather than quic-go's default CUBIC (D-14, D-16). The
// controller matters more here than in a plain file-transfer tool because
// D-14 keeps everything on one connection per peer: bulk transfer, clipboard,
// input and eventually mirror datagrams all share a single congestion
// controller and a single pacer. Whatever queue that controller builds is
// added to the latency of every interactive capability riding alongside it.
//
// See PROVENANCE.md for what is vendored here and why it could not be a
// dependency.
package congestion

import (
	"github.com/apernet/quic-go"

	"github.com/shreyashsri79/openair/internal/congestion/bbr"
)

// DefaultProfile is the gain profile OpenAir installs unless told otherwise.
//
// BBRv1's standing queue -- roughly 2xBDP inflight -- and its ProbeRTT dips
// are the one v1 defect that genuinely bites this architecture, because of the
// shared connection described above. D-16 identified the mitigation as a knob
// rather than a rewrite: a gain profile trading peak throughput for a shorter
// queue. "conservative" is that profile (highGain 2.25 against the 2.885
// default, plus drain-to-target and overshoot detection).
//
// This is a latency-first default, chosen because the HLD's sub-50 ms
// glass-to-glass target and its requirement that input never queue behind bulk
// are harder to recover than the throughput given up. Callers moving bulk on a
// path with no interactive traffic may pass ProfileStandard instead.
const DefaultProfile = bbr.ProfileConservative

// UseBBR installs a BBR sender on an established connection, replacing the
// default controller.
//
// It must be called after the QUIC handshake completes: the initial packet
// size is derived from the remote address, and the connection has no sent
// packet handler to install into before then.
func UseBBR(conn *quic.Conn, profile bbr.Profile) {
	conn.SetCongestionControl(bbr.NewBbrSender(
		bbr.DefaultClock{},
		bbr.GetInitialPacketSize(conn.RemoteAddr()),
		profile,
	))
}

// Use installs the default profile. This is what conn.Dialer and conn.Listener
// call; taking the profile from one place keeps the choice reviewable.
func Use(conn *quic.Conn) {
	UseBBR(conn, DefaultProfile)
}

// ParseProfile maps a configuration string onto a profile. An empty string is
// not an error and yields ProfileStandard, matching upstream; callers wanting
// OpenAir's default must use DefaultProfile explicitly.
func ParseProfile(s string) (bbr.Profile, error) { return bbr.ParseProfile(s) }
