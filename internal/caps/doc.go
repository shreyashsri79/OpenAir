// Package caps defines the capability contract and hosts the implementations.
//
// A capability declares an id, the trust level it requires, and how it serves
// inbound messages. It sees a session -- open a stream, send a datagram, query a
// path hint -- and cannot tell whether the path is LAN, punched or relayed.
//
// Phase 1 ships files and clipboard. remotefs and notifications are Phase 3;
// input and mirror are Phase 4, and mirror's framing is provisional pending D-9.
package caps
