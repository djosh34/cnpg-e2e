// Command mirror copies the images and charts of versions/versions.yaml
// into this repo's packages, from Docker Hardened Images and the upstream
// barman-cloud chart, and writes the digests it copied into the file. With
// -latest it takes the newest tag of each with the same first number and
// suffix, such as 18.8-debian13 for 18.7-debian13: the nightly runs it so.
//
//	go run ./cmd/mirror [-latest]
//
// It needs docker login to dhi.io, and to ghcr.io with the right to push
// this repo's packages.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/djosh34/cnpg-e2e/e2e/registry"
)

func main() {
	latest := flag.Bool("latest", false, "copy the newest tag of the same kind of each image and chart")
	flag.Parse()
	if err := registry.Mirror(context.Background(), *latest); err != nil {
		log.Fatal("mirror: ", err)
	}
}
