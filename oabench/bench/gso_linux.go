//go:build linux

package bench

import "os"

// gsoState reports whether quic-go's segmentation offload is in play.
//
// Linux is the only platform where quic-go implements UDP_SEGMENT, so it is the
// only one where this can be "on" -- and the only one where the env var can
// turn it off to emulate the others.
func gsoState() string {
	if os.Getenv("QUIC_GO_DISABLE_GSO") != "" {
		return "off"
	}
	return "on"
}
