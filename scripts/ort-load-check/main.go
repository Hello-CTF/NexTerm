// Command ort-load-check loads the ONNX Runtime from a packaged ai-runtime
// directory and prints "OK <version>". Release jobs run it against the
// extracted installer, deb or signed app to prove the shipped runtime loads.
package main

import (
	"fmt"
	"os"

	"github.com/Hello-CTF/NexTerm/internal/ai/privacy/inference"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: ort-load-check <ai-runtime directory>")
		os.Exit(2)
	}
	version, err := inference.LoadRuntime(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	if version == "" {
		fmt.Fprintln(os.Stderr, "FAIL: runtime loaded but reported no version")
		os.Exit(1)
	}
	fmt.Println("OK", version)
}
