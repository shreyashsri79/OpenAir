package discovery

import (
	"context"
	"fmt"
	"time"

	"github.com/grandcat/zeroconf"
)

func DiscoverAndroid() (string, int, error) {
	fmt.Println("🔍 Scanning for OpenAir Android device...")
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

	select {
	case entry := <-entries:
		// Prefer IPv4
		addr := entry.AddrIPv4[0].String()
		fmt.Printf("✅ Found: %s at %s:%d\n", entry.Instance, addr, entry.Port)
		return addr, entry.Port, nil
	case <-ctx.Done():
		return "", 0, fmt.Errorf("discovery timed out")
	}
}
