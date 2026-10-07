// Command probe reports what the environment package finds on this host.
//
// It exists as an executable demonstration and as a manual verification tool:
// run it to see the real indicators for the machine you are on.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/hivemind/bininspect/environment"
)

func main() {
	snap := environment.New(environment.Options{IncludeHostnames: false}).Probe()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(snap); err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "\n%s\n", snap.Summary)
}
