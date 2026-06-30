// Command kowloon-api is the HTTP front of the semantic-memory service
// Lady Glass's index-kowloon stage talks to.
//
// At this stage of the build-out the binary boots with a stub Service
// that returns 501 on every business endpoint — the indexer (which
// composes source + schema + embed + backend) lands in a follow-up
// commit. /healthz already returns 200 so deploy plumbing can be tested
// in advance.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/keix/kowloon"
	"github.com/keix/kowloon/internal/httpapi"
)

func main() {
	addr := env("KOWLOON_ADDR", ":8080")

	server := httpapi.NewServer(unimplemented{})

	log.Printf("kowloon-api listening on %s", addr)
	if err := http.ListenAndServe(addr, server.Routes()); err != nil {
		log.Fatal(err)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// unimplemented is the placeholder Service used until the indexer
// commit wires a real one. Every business endpoint reports 501 so
// stubbed builds are distinguishable from real failures.
type unimplemented struct{}

func (unimplemented) IndexResult(context.Context, kowloon.IndexResultRequest) (kowloon.IndexResultResponse, error) {
	return kowloon.IndexResultResponse{}, kowloon.ErrNotImplemented
}

func (unimplemented) Search(context.Context, kowloon.SearchRequest) (kowloon.SearchResponse, error) {
	return kowloon.SearchResponse{}, kowloon.ErrNotImplemented
}

func (unimplemented) ResolveMerchant(context.Context, kowloon.ResolveMerchantRequest) (kowloon.ResolveMerchantResponse, error) {
	return kowloon.ResolveMerchantResponse{}, kowloon.ErrNotImplemented
}

func (unimplemented) DeleteJob(context.Context, string) error {
	return kowloon.ErrNotImplemented
}
