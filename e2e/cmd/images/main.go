// Command images builds and pushes each image of e2e/images whose package
// does not have its tag yet:
//
//	go -C e2e run ./cmd/images
//
// It needs docker login to ghcr.io with the right to push this repo's
// packages: only this repo's workflows push them.
package main

import (
	"context"
	"log"

	"github.com/djosh34/cnpg-e2e/e2e/images"
	"github.com/djosh34/cnpg-e2e/e2e/registry"
)

func main() {
	if err := images.Publish(context.Background(), registry.Client()); err != nil {
		log.Fatal("images: ", err)
	}
}
