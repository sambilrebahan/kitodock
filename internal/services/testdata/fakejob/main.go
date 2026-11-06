// Command fakejob is a test fixture impersonating a supervised service.
//
//	fakejob hold          run until killed, printing a heartbeat line
//	fakejob hold <tag>    same, tagging every heartbeat
//	fakejob quit          exit immediately (crash-loop stand-in)
//	fakejob child         spawn `hold child` and hold (tree-kill stand-in)
//
// Every mode announces pids so tests can watch them disappear.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

func main() {
	mode := "hold"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "quit":
		fmt.Printf("fakejob %d exiting\n", os.Getpid())
		os.Exit(1)
	case "child":
		child := exec.Command(os.Args[0], "hold", "child")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(1)
		}
		fmt.Printf("child pid %d\n", child.Process.Pid)
		hold("parent")
	default:
		tag := "solo"
		if len(os.Args) > 2 {
			tag = os.Args[2]
		}
		hold(tag)
	}
}

func hold(tag string) {
	fmt.Printf("fakejob %s pid %d ready\n", tag, os.Getpid())
	for range time.Tick(100 * time.Millisecond) {
		fmt.Printf("beat %s %d\n", tag, os.Getpid())
	}
}
