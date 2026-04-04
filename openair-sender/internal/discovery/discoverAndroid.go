package discovery

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

func DiscoverAndroid() (string, int, error) {
	fmt.Println("Scanning for OpenAir device...")

	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return "", 0, err
	}

	entries := make(chan *zeroconf.ServiceEntry)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		err = resolver.Browse(ctx, "_openair._tcp", "local.", entries)
		if err != nil {
			fmt.Println("Browse failed:", err)
		}
	}()

	for {
		select {
		case entry := <-entries:
			// 🔥 Pick correct IP (ignore virtual adapters)
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

			// fallback
			if addr == "" && len(entry.AddrIPv4) > 0 {
				addr = entry.AddrIPv4[0].String()
			}

			if addr == "" {
				continue
			}

			fmt.Printf("Found: %s at %s:%d\n", entry.Instance, addr, entry.Port)
			return addr, entry.Port, nil

		case <-ctx.Done():
			return "", 0, fmt.Errorf("discovery timed out")
		}
	}
}
