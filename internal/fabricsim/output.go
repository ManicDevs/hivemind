package fabricsim

import (
	"io"
	"os"
)

// defaultStdout is the fallback output when no writer is configured.
//
// It lives in its own file, apart from the simulation core, so that the
// topology, transport, attack generation and defence cannot reach the outside
// world even transitively. The zero-write guarantee is a structural property
// of the package, not a runtime counter: TestCorePackageCannotTouchTheHost
// enforces the split.
var defaultStdout io.Writer = os.Stdout
