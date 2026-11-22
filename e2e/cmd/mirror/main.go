// Command mirror copies the images and charts of a versions file into this
// repo's packages, from Docker Hardened Images and the upstream barman-cloud
// chart, by the tags the file names, and writes the digests it copied into
// the file:
//
//	go -C e2e run ./cmd/mirror ../versions/versions.yaml
//
// It needs docker login to dhi.io, and to ghcr.io with the right to push
// this repo's packages: only this repo's workflows push them.
package main

import (
	"context"
	"log"
	"os"

	"github.com/djosh34/cnpg-e2e/e2e/registry"
)

func main() {
	if len(os.Args) != 2 {
		log.Print("usage: mirror <versions file>")
		os.Exit(2)
	}
	if err := registry.Mirror(context.Background(), registry.Client(), os.Args[1]); err != nil {
		log.Fatal("mirror: ", err)
	}
}
