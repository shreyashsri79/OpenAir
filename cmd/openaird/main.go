// Command openaird is the OpenAir daemon: always-on, one per device.
//
// Desktop shells talk to it over local IPC using the same envelope and messages
// as the network protocol, not gRPC (D-29). Android hosts the same core
// in-process via gomobile instead (D-31), so it has no IPC at all.
package main

func main() {}
