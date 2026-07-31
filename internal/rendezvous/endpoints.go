package rendezvous

import (
	"fmt"
	"net"
	"sort"
	"strconv"
)

// LocalEndpoints lists the addresses this host can be reached on at port,
// formatted as "ip:port" for §16's endpoint list.
//
// Loopback and link-local addresses are left out: a peer on another network
// cannot use them, and publishing them to a rendezvous server tells the
// operator something about this machine's interfaces for no benefit. IPv6
// addresses are bracketed by net.JoinHostPort, which is what the endpoint
// format needs and what hand-built string concatenation gets wrong.
//
// This is the local half only. The reflexive address — what the world sees
// after NAT — comes from STUN, which is M9's business (§18); until then a
// device behind NAT is reachable through its relay home rather than through
// these.
func LocalEndpoints(port int) ([]string, error) {
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("rendezvous: port %d is out of range", port)
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("rendezvous: list interfaces: %w", err)
	}

	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
				continue
			}
			out = append(out, net.JoinHostPort(ip.String(), fmt.Sprint(port)))
		}
	}
	// Sorted so a re-registration with an unchanged interface set produces the
	// same signed bytes, rather than a different order every few minutes.
	sort.Strings(out)
	return out, nil
}

// EndpointsFor turns the address a listener actually bound into the endpoint
// list to publish.
//
// A daemon bound to a specific address is reachable at that address and nowhere
// else, so publishing the machine's other interfaces would advertise addresses
// that refuse every connection made to them. Only an unspecified bind (":9000",
// "0.0.0.0:9000", "[::]:9000") means "every interface", and only then is
// enumerating them the right answer.
func EndpointsFor(bound string) ([]string, error) {
	host, portStr, err := net.SplitHostPort(bound)
	if err != nil {
		return nil, fmt.Errorf("rendezvous: bound address %q: %w", bound, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("rendezvous: bound port %q: %w", portStr, err)
	}
	if host != "" {
		if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() {
			return []string{net.JoinHostPort(ip.String(), portStr)}, nil
		}
	}
	return LocalEndpoints(port)
}
