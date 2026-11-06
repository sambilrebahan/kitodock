// Command probe is a test fixture standing in for a real service binary.
// Its exit behavior is selected through KITODOCK_PROBE_MODE:
//
//	ok           prints a version and exits 0
//	dll-missing  exits with STATUS_DLL_NOT_FOUND, like a binary whose
//	             VC++ runtime DLLs are absent
//	stale        prints PHP's "not compatible with this build linked with"
//	             complaint and exits 1, like a runtime generation too old
//	crash        exits 3 saying nothing, like an unrelated failure
package main

import (
	"fmt"
	"os"
)

func main() {
	switch os.Getenv("KITODOCK_PROBE_MODE") {
	case "dll-missing":
		os.Exit(0xC0000135)
	case "stale":
		fmt.Println("php.exe: OKS is not compatible with this PHP build linked with VS17 runtime")
		os.Exit(1)
	case "crash":
		os.Exit(3)
	default:
		fmt.Println("1.2.3")
	}
}
