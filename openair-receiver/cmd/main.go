package main

import (
	"fmt"
	"net"
	"strconv"

	connhandler "github.com/shreyashsri79/openair-receiver/connHandler"
	"github.com/shreyashsri79/openair-receiver/constants"
	errorhandler "github.com/shreyashsri79/openair-receiver/errorHandler"
)

func main() {
	addr := ":" + strconv.Itoa(constants.PORT)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		errorhandler.FatalRed("failed to bind on port :"+strconv.Itoa(constants.PORT)+" ", err)
		return
	}
	defer ln.Close()

	fmt.Println("\033[32mListening on port :" + strconv.Itoa(constants.PORT) + "\033[0m")

	// Start mDNS
	mdns, err := startMDNS("Zoro Fedora", constants.PORT)
	if err != nil {
		errorhandler.FatalRed("mdns error", err)
	}
	defer mdns.Shutdown()

	// Create file ONCE and share across handlers
	file, err := connhandler.InitFile("received_file")
	if err != nil {
		errorhandler.FatalRed("file init error", err)
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			errorhandler.FatalRed("failed to accept conn ", err)
			continue
		}

		go connhandler.HandleConn(conn, file)
	}
}