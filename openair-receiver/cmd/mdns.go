package main

import (
	"fmt"

	"github.com/grandcat/zeroconf"
)

func startMDNS(instanceName string, port int) (*zeroconf.Server, error) {
	txt := []string{
		"app=OpenAir",
		"ver=1",
	}

	server, err := zeroconf.Register(
		instanceName,     // Visible name in Android discovery list
		"_openair._tcp",  // Service type
		"local.",         // Domain
		port,             // Port
		txt,              // TXT records
		nil,              // Interfaces (nil = all)
	)
	if err != nil {
		return nil, err
	}

	fmt.Printf("\033[32mmDNS active:\033[0m %s._openair._tcp.local (port %d)\n", instanceName, port)
	return server, nil
}
