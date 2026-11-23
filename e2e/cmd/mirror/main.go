// Command mirror copies the images and charts of a versions file into this
// repo's packages, from Docker Hardened Images and the upstream barman-cloud
// chart, by the tags the file names, and writes the digests it copied into
// the file:
//
//	go -C e2e run ./cmd/mirror ../versions/versions.yaml
//
// With -latest it copies the newest tag of each image and chart that has
// the same first version number and the same suffix, such as 18.8-debian13
// for 18.7-debian13, and writes those tags too. The nightly runs it so.
//
// It needs docker login to dhi.io, and to ghcr.io with the right to push
// this repo's packages: only this repo's workflows push them.
package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/djosh34/cnpg-e2e/e2e/registry"
)

func main() {
	latest := flag.Bool("latest", false, "copy the newest tag of the same kind of each image and chart")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Print("usage: mirror [-latest] <versions file>")
		os.Exit(2)
	}
	if err := registry.Mirror(context.Background(), flag.Arg(0), *latest); err != nil {
		log.Fatal("mirror: ", err)
	}
}
