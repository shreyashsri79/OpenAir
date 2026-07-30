//go:build !linux

package bench

// gsoState is always "none" off Linux. quic-go implements UDP_SEGMENT only in
// sys_conn_helper_linux.go; every other platform falls through to a stub, so
// there is no offload to enable and QUIC_GO_DISABLE_GSO has nothing to disable.
// Reporting "on" here -- as an earlier revision did, by reading the env var on
// every platform -- mislabelled the first Windows results.
func gsoState() string { return "none" }
