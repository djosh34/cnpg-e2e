// Command fill runs outside the airgap, from the root of the checkout,
// before ansible/install.yml: it downloads every file of
// versions/versions.yaml into files/, builds the wrapper charts of helm/,
// and copies every image into the registry the nodes read, such as the
// writable Distribution on their S3 bucket.
//
//	go run ./cmd/fill [-plain-http] <registry host:port>
package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/djosh34/cnpg-e2e/e2e/fill"
	"github.com/djosh34/cnpg-e2e/e2e/versions"
)

func main() {
	plain := flag.Bool("plain-http", false, "reach the registry over HTTP")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: fill [-plain-http] <registry host:port>")
	}
	data, err := os.ReadFile(versions.File)
	if err == nil {
		err = fill.Run(context.Background(), data, flag.Arg(0), *plain)
	}
	if err != nil {
		log.Fatal("fill: ", err)
	}
}
