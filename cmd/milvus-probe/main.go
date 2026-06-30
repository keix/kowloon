// Command milvus-probe is a one-shot connectivity check for the local
// docker-compose Milvus stack. It connects with the Go SDK we will later
// reuse from internal/backend/milvus, lists collections, and exits — no
// schema changes, no inserts. Useful before wiring the backend adapter
// into the API.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/milvus-io/milvus-sdk-go/v2/client"
)

func main() {
	endpoint := envOr("MILVUS_ENDPOINT", "127.0.0.1:19530")

	ctx := context.Background()
	c, err := client.NewClient(ctx, client.Config{Address: endpoint})
	if err != nil {
		log.Fatalf("connect %s: %v", endpoint, err)
	}
	defer c.Close()

	cols, err := c.ListCollections(ctx)
	if err != nil {
		log.Fatalf("list collections: %v", err)
	}

	fmt.Printf("connected to %s\n", endpoint)
	fmt.Printf("collections: %d\n", len(cols))
	for _, col := range cols {
		fmt.Printf("  - %s\n", col.Name)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
