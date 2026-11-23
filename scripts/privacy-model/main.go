// Command privacy-model downloads the latest FP32 AstrLink Guard release through
// the verified catalog the application uses and prints the installation
// directory. CI runs the real-model engine tests against it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/Hello-CTF/NexTerm/internal/ai/privacy"
)

func main() {
	root := flag.String("root", "", "directory that receives the installation")
	flag.Parse()
	if *root == "" {
		fmt.Fprintln(os.Stderr, "usage: privacy-model -root <directory>")
		os.Exit(2)
	}
	ctx := context.Background()
	catalog := privacy.NewCatalog()
	release, err := catalog.Latest(ctx)
	if err == nil {
		var installation privacy.Installation
		installation, err = catalog.Manifest(ctx, release, privacy.FP32)
		if err == nil {
			var directory string
			directory, err = catalog.Download(ctx, *root, installation, nil, nil)
			if err == nil {
				fmt.Println(directory)
				return
			}
		}
	}
	fmt.Fprintln(os.Stderr, "privacy-model:", err)
	os.Exit(1)
}
