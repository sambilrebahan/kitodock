// Command devhost is a test fixture impersonating a dev server: it prints
// port lines like real dev servers do, then holds that port open.
//
//	go run testdata/devhost/main.go bare          listen on :0, print, hold
//	go run testdata/devhost/main.go vite <port>   print "Local:" line, hold
//	go run testdata/devhost/main.go app <port>    print "Server running on", hold
package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"time"
)

func main() {
	mode := "bare"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	var port int
	if len(os.Args) > 2 {
		port, _ = strconv.Atoi(os.Args[2])
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(1)
	}
	actual := listener.Addr().(*net.TCPAddr).Port
	if port > 0 {
		// Try the requested port; fall back to ephemeral.
		if want, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			listener.Close()
			listener = want
			actual = port
		}
	}

	switch mode {
	case "vite":
		fmt.Printf("  ➜  Local:   http://localhost:%d/\n", actual)
	case "app":
		fmt.Printf("  Server running on http://127.0.0.1:%d\n", actual)
	default:
		fmt.Printf("listening at http://localhost:%d\n", actual)
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		conn.Close()
	}
}
