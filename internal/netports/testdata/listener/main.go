// Command listener is a test fixture: it listens on 127.0.0.1 on an
// ephemeral port, prints the port number, then holds the socket open until
// killed. It stands in for a dev server whose port the UI wants back.
package main

import (
	"fmt"
	"net"
	"os"
)

func main() {
	socket, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer socket.Close()
	fmt.Println(socket.Addr().(*net.TCPAddr).Port)
	for {
		conn, err := socket.Accept()
		if err != nil {
			return
		}
		conn.Close()
	}
}
