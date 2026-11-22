// Command fill copies every image a versions file pins into a registry,
// keeping each digest: in production, the writable Distribution on the S3
// bucket that the registry of every node reads.
//
//	go -C e2e run ./cmd/fill [-plain-http] <versions file> <registry host:port>
//
// The e2e runner fills its registry with the same code. -plain-http reaches
// the registry over HTTP instead of HTTPS.
package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/djosh34/cnpg-e2e/e2e/registry"
	"github.com/djosh34/cnpg-e2e/e2e/versions"
)

func main() {
	plain := flag.Bool("plain-http", false, "reach the registry over HTTP")
	flag.Parse()
	if flag.NArg() != 2 {
		log.Print("usage: fill [-plain-http] <versions file> <registry host:port>")
		os.Exit(2)
	}
	if err := fill(flag.Arg(0), flag.Arg(1), *plain); err != nil {
		log.Fatal("fill: ", err)
	}
}

func fill(file, host string, plain bool) error {
	m, err := versions.Load(file)
	if err != nil {
		return err
	}
	var plainHTTP []string
	if plain {
		plainHTTP = append(plainHTTP, host)
	}
	return registry.Fill(context.Background(), registry.Client(plainHTTP...), m, host)
}
