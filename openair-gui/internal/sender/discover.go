package sender

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

var OnLog = func(s string) { fmt.Println(s) }

// Device is one discovered OpenAir receiver on the LAN.
type Device struct {
	Name string
	Host string
	Port int
}

func (d Device) Key() string { return fmt.Sprintf("%s@%s:%d", d.Name, d.Host, d.Port) }

// localIPSet returns every IP bound to this machine's interfaces, so we can
// hide our own receiver from the device list.
func localIPSet() map[string]struct{} {
	set := map[string]struct{}{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return set
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			set[ipn.IP.String()] = struct{}{}
		}
	}
	return set
}

func pickAddr(entry *zeroconf.ServiceEntry) string {
	var addr string
	for _, ip := range entry.AddrIPv4 {
		ipStr := ip.String()

		// Prefer real LAN IPs
		if strings.HasPrefix(ipStr, "192.168.") ||
			strings.HasPrefix(ipStr, "10.") ||
			strings.HasPrefix(ipStr, "172.") {

			// Skip known bad ranges (VirtualBox etc.)
			if strings.HasPrefix(ipStr, "192.168.56.") {
				continue
			}

			addr = ipStr
			break
		}
	}
	if addr == "" && len(entry.AddrIPv4) > 0 {
		addr = entry.AddrIPv4[0].String()
	}
	return addr
}

// DiscoverOnce browses mDNS for one scan window and returns every OpenAir
// receiver found, excluding any that resolves to this machine itself.
func DiscoverOnce(timeout time.Duration) []Device {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		OnLog(fmt.Sprintf("Discovery error: %v", err))
		return nil
	}

	entries := make(chan *zeroconf.ServiceEntry)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	go func() {
		if err := resolver.Browse(ctx, "_openair._tcp", "local.", entries); err != nil {
			OnLog(fmt.Sprintf("Browse failed: %v", err))
			cancel()
		}
	}()

	local := localIPSet()
	seen := map[string]Device{}

	for {
		select {
		case entry, ok := <-entries:
			if !ok {
				entries = nil
				continue
			}
			if entry == nil {
				continue
			}
			addr := pickAddr(entry)
			if addr == "" {
				continue
			}
			if _, self := local[addr]; self {
				continue // our own receiver — never show the host device
			}
			d := Device{Name: entry.Instance, Host: addr, Port: entry.Port}
			seen[d.Key()] = d

		case <-ctx.Done():
			out := make([]Device, 0, len(seen))
			for _, d := range seen {
				out = append(out, d)
			}
			sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
			return out
		}
	}
}

// DiscoverLoop scans continuously until ctx is cancelled, invoking onUpdate
// with the full current device list after every scan window.
func DiscoverLoop(ctx context.Context, scanWindow, pause time.Duration, onUpdate func([]Device)) {
	for {
		devs := DiscoverOnce(scanWindow)
		select {
		case <-ctx.Done():
			return
		default:
		}
		onUpdate(devs)
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
	}
}
