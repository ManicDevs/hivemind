// hivemind-image emits the bare-metal HIVEMIND kernel as a multiboot-1 raw
// image without invoking a C compiler or assembler.
package main

import (
	"flag"
	"os"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/kernel/bootimage"
)

func main() {
	out := flag.String("out", "/tmp/hivemind-kernel.bin", "path for the raw boot image")
	flag.Parse()
	img, err := bootimage.Build()
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(*out, img.Bytes, 0o644); err != nil {
		panic(err)
	}
}
