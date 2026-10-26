// Command shim1demo supplies the stubs and the test client for the Shim 1 demo.
//
// Shim 1 is ONIX configured as an ONDC seller app (see config/shim1). The demo
// needs the parts of the ONDC network that we do not have, so this program stands
// in for them:
//
//	keys     make demo keys, a stub ONDC Registry file and the Shim 1 config
//	backend  stub seller business logic: answers /search, then sends on_search
//	bap      stub buyer app: receives on_search and checks its signature
//	client   plays the buyer app and the Gateway; runs the test scenarios
//
// run-shim1-demo.sh at the repository root runs all of them together.
package main

import (
	"fmt"
	"os"
)

const usage = `usage: shim1demo <command> [flags]

commands:
  keys      generate demo keys, stub registry and Shim 1 config
  backend   run the stub seller backend
  bap       run the stub buyer app
  client    run the test scenarios against Shim 1`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keys":
		err = runKeys(os.Args[2:])
	case "backend":
		err = runBackend(os.Args[2:])
	case "bap":
		err = runBAP(os.Args[2:])
	case "client":
		err = runClient(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
