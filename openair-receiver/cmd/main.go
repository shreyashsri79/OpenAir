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
	fmt.Println("\033[32mListening on port :"+strconv.Itoa(constants.PORT)+"\033[0m")
	defer ln.Close()

	for {
		conn, err := ln.Accept()
		if err != nil {
			errorhandler.FatalRed("failed to accept conn ", err)
			continue
		}
		connhandler.HandleConn(conn)
	}
}
