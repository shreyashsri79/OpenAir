package discovery

import (
	"context"
	"errors"
	"net"
	"time"
)

// unicastSource is PROTOCOL.md §15.2: the fallback for networks that suppress
// multicast. It does two things §15.2 permits and mDNS cannot do there --
// broadcast an announce to the subnet, and probe known-last-good addresses
// directly.
//
// It shares one socket for sending and receiving, so a peer that answers a
// query replies to a port this device is already listening on. That is what
// makes the fallback work through a NAT-less but multicast-filtered switch:
// the exchange is ordinary unicast UDP in both directions after the first
// datagram.
type unicastSource struct {
	cfg  *Config
	self Announce
	conn *net.UDPConn

	// broadcastOK records whether SO_BROADCAST was actually granted. If it
	// was not, UnicastPeers still work and the source is still useful.
	broadcastOK bool
}

func newUnicastSource(cfg *Config, self Announce) (*unicastSource, error) {
	conn, broadcastOK, err := listenBeacon(cfg.unicastPort())
	if err != nil {
		return nil, err
	}
	if !broadcastOK {
		cfg.logf("discovery: unicast fallback has no broadcast permission; direct peers only")
	}
	return &unicastSource{cfg: cfg, self: self, conn: conn, broadcastOK: broadcastOK}, nil
}

// listenBeacon binds the fallback port and asks for broadcast permission.
//
// The socket is bound to the wildcard address on purpose: a broadcast datagram
// is delivered only to sockets bound to the wildcard or to the broadcast
// address itself, so binding a specific interface address would make this deaf
// to exactly the traffic it exists to hear.
func listenBeacon(port int) (*net.UDPConn, bool, error) {
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		return nil, false, err
	}
	broadcastOK := true
	if err := setBroadcast(pc); err != nil {
		// Sending to a broadcast address without SO_BROADCAST fails with
		// EACCES on Linux. Losing broadcast is a degradation, not a failure:
		// the direct-probe half of §15.2 is unaffected.
		broadcastOK = false
	}
	return pc, broadcastOK, nil
}

// run beacons and listens until ctx is cancelled.
func (u *unicastSource) run(ctx context.Context, sink func(Announce, []string, Via)) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		u.readLoop(ctx, sink)
	}()

	// Beacon immediately: PRD R6's three seconds is measured from process
	// start, and waiting out one interval before saying anything would spend
	// most of the budget on nothing.
	u.beacon()
	interval := u.cfg.scanWindow() + u.cfg.pause()
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			u.conn.Close()
			<-done
			return
		case <-t.C:
			u.beacon()
		}
	}
}

// beacon sends a query and this device's own announce to every target. Both
// are sent because the two solve different halves of the problem: the query
// makes quiet peers speak up, and the announce reaches a peer that started
// after our last query and has not yet asked its own.
func (u *unicastSource) beacon() {
	query := EncodeQuery(u.self.DeviceID)

	// A browse-only instance asks and never answers: it has no listening port,
	// so an announce from it would publish an address that refuses every
	// connection made to it.
	var announce []byte
	if !u.cfg.BrowseOnly {
		announce = EncodeAnnounce(u.self)
	}

	for _, target := range u.targets() {
		u.send(query, target)
		if announce != nil {
			u.send(announce, target)
		}
	}
}

// targets is every address the beacon goes to: the configured known-last-good
// peers first, then subnet broadcast if the socket was granted it.
func (u *unicastSource) targets() []*net.UDPAddr {
	var out []*net.UDPAddr
	for _, p := range u.cfg.UnicastPeers {
		addr, err := net.ResolveUDPAddr("udp4", p)
		if err != nil {
			u.cfg.logf("discovery: unicast peer %q: %v", p, err)
			continue
		}
		out = append(out, addr)
	}
	if u.cfg.DisableBroadcast || !u.broadcastOK {
		return out
	}
	for _, ip := range broadcastAddrs() {
		out = append(out, &net.UDPAddr{IP: ip, Port: u.cfg.unicastPort()})
	}
	return out
}

func (u *unicastSource) send(b []byte, to *net.UDPAddr) {
	if _, err := u.conn.WriteToUDP(b, to); err != nil {
		// One unreachable interface must not stop the others. Errors here are
		// routine on a laptop with a down VPN adapter.
		u.cfg.logf("discovery: unicast send to %s: %v", to, err)
	}
}

func (u *unicastSource) readLoop(ctx context.Context, sink func(Announce, []string, Via)) {
	buf := make([]byte, maxDatagram)
	for {
		n, src, err := u.conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			u.cfg.logf("discovery: unicast read: %v", err)
			continue
		}

		d, err := decodeDatagram(buf[:n])
		if err != nil {
			// Anyone can write to this port. A datagram we cannot parse is
			// not an incident, and logging every one of them would be a log
			// amplification vector.
			continue
		}

		switch {
		case d.isQuery:
			if d.queryFrom == u.self.DeviceID {
				continue // our own broadcast, looped back
			}
			if u.cfg.BrowseOnly {
				continue // nothing to say; see beacon
			}
			// Answer unicast to the asker, not to the broadcast address:
			// nobody else asked.
			u.send(EncodeAnnounce(u.self), src)
		default:
			if d.announce.DeviceID == u.self.DeviceID {
				continue
			}
			addrs := hostPortsFromAddr(src, d.announce.Port)
			if len(addrs) == 0 {
				continue
			}
			sink(d.announce, addrs, ViaUnicast)
		}
	}
}

func (u *unicastSource) close() { u.conn.Close() }

// localPort reports the port the beacon socket actually bound. Tests use it;
// production reads it from Config.
func (u *unicastSource) localPort() int {
	if a, ok := u.conn.LocalAddr().(*net.UDPAddr); ok {
		return a.Port
	}
	return 0
}

// broadcastAddrs computes the subnet-directed broadcast address of every up,
// non-loopback IPv4 interface, plus the limited broadcast address.
//
// Subnet-directed broadcast is listed first because 255.255.255.255 is dropped
// by a good many managed switches and by Windows Firewall profiles, while
// 192.168.1.255 usually survives -- and a network that filters multicast is
// exactly the sort that filters the limited broadcast too.
func broadcastAddrs() []net.IP {
	var out []net.IP
	ifaces, err := net.Interfaces()
	if err != nil {
		return []net.IP{net.IPv4bcast}
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			v4 := ipn.IP.To4()
			if v4 == nil {
				continue
			}
			mask := net.IP(ipn.Mask).To4()
			if mask == nil {
				continue
			}
			b := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				b[i] = v4[i] | ^mask[i]
			}
			out = append(out, b)
		}
	}
	return append(out, net.IPv4bcast)
}
