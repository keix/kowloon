// Package dynamodb is the persistent implementation of
// internal/embed/cache.Cache. It stores vectors in DynamoDB, keyed by
// the SHA-256 hash the cache package builds, and survives kowloon-api
// restarts — the value the in-process memory LRU cannot provide.
//
// Vectors are stored as big-endian float32 bytes in the Binary DDB
// attribute. 3072-dim vectors are ~12 KB per entry, well under DDB's
// 400 KB item limit and cheaper than JSON encoding.
package dynamodb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/keix/kowloon/internal/embed/cache"
)

// Client is the subset of *awsdynamodb.Client the cache needs. Split
// out so tests can drive the cache with a fake without a live DDB.
type Client interface {
	GetItem(ctx context.Context, params *awsdynamodb.GetItemInput, optFns ...func(*awsdynamodb.Options)) (*awsdynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, params *awsdynamodb.PutItemInput, optFns ...func(*awsdynamodb.Options)) (*awsdynamodb.PutItemOutput, error)
}

type Config struct {
	Client    Client
	TableName string
	// TTL is the retention window for cache entries. Zero disables
	// TTL — but the DDB table declares an expires_at TTL attribute,
	// so leaving it zero means entries live forever which defeats
	// half the reason to have TTL in the first place. 90 days is a
	// sensible default (long enough that re-index against the same
	// model is free, short enough that a stale embedding-model
	// swap does not keep zombie vectors around forever).
	TTL time.Duration
}

type Cache struct {
	client    Client
	tableName string
	ttl       time.Duration
}

func New(cfg Config) *Cache {
	return &Cache{
		client:    cfg.Client,
		tableName: cfg.TableName,
		ttl:       cfg.TTL,
	}
}

func (c *Cache) Get(ctx context.Context, key cache.Key) ([]float32, bool, error) {
	out, err := c.client.GetItem(ctx, &awsdynamodb.GetItemInput{
		TableName: aws.String(c.tableName),
		Key: map[string]types.AttributeValue{
			"cache_key": &types.AttributeValueMemberS{Value: key.String()},
		},
	})
	if err != nil {
		return nil, false, fmt.Errorf("dynamodb cache get: %w", err)
	}
	if out.Item == nil {
		return nil, false, nil
	}
	vecAttr, ok := out.Item["vector"].(*types.AttributeValueMemberB)
	if !ok {
		return nil, false, errors.New("dynamodb cache: missing vector attribute")
	}
	return decodeVector(vecAttr.Value), true, nil
}

func (c *Cache) Put(ctx context.Context, key cache.Key, vec []float32) error {
	item := map[string]types.AttributeValue{
		"cache_key": &types.AttributeValueMemberS{Value: key.String()},
		"model":     &types.AttributeValueMemberS{Value: key.Model},
		"dim":       &types.AttributeValueMemberN{Value: strconv.Itoa(key.Dimensions)},
		"vector":    &types.AttributeValueMemberB{Value: encodeVector(vec)},
	}
	if c.ttl > 0 {
		item["expires_at"] = &types.AttributeValueMemberN{
			Value: strconv.FormatInt(time.Now().Add(c.ttl).Unix(), 10),
		}
	}
	if _, err := c.client.PutItem(ctx, &awsdynamodb.PutItemInput{
		TableName: aws.String(c.tableName),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("dynamodb cache put: %w", err)
	}
	return nil
}

// encodeVector packs []float32 into big-endian bytes. Deterministic
// across architectures and cheap to decode.
func encodeVector(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.BigEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

func decodeVector(b []byte) []float32 {
	n := len(b) / 4
	v := make([]float32, n)
	for i := 0; i < n; i++ {
		v[i] = math.Float32frombits(binary.BigEndian.Uint32(b[i*4:]))
	}
	return v
}
