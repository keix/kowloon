// Package dynamodb is the persistent implementation of
// internal/idempotency.Store. Where the memory implementation loses
// state on restart, this one keeps the (job_id, result_uri, ...,
// content_hash) → IndexResultResponse mapping in DynamoDB so a
// Kowloon restart mid-flight does not turn a duplicate POST from
// Lady Glass into a re-run.
//
// The stored value is the JSON-encoded IndexResultResponse — small
// enough to fit trivially inside an item, and human-readable in the
// DDB console which helps when debugging why a retry did not dedupe.
package dynamodb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/keix/kowloon"
	"github.com/keix/kowloon/internal/idempotency"
)

type Client interface {
	GetItem(ctx context.Context, params *awsdynamodb.GetItemInput, optFns ...func(*awsdynamodb.Options)) (*awsdynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, params *awsdynamodb.PutItemInput, optFns ...func(*awsdynamodb.Options)) (*awsdynamodb.PutItemOutput, error)
}

type Config struct {
	Client    Client
	TableName string
	// TTL is the retention window for idempotency entries. Lady
	// Glass workflow retries typically happen within minutes;
	// keeping entries much longer (weeks) mostly protects against
	// operator-initiated re-runs after a bug fix. Default 30 days.
	TTL time.Duration
}

type Store struct {
	client    Client
	tableName string
	ttl       time.Duration
}

func New(cfg Config) *Store {
	return &Store{
		client:    cfg.Client,
		tableName: cfg.TableName,
		ttl:       cfg.TTL,
	}
}

func (s *Store) Lookup(ctx context.Context, key idempotency.Key) (kowloon.IndexResultResponse, bool, error) {
	out, err := s.client.GetItem(ctx, &awsdynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"idempotency_key": &types.AttributeValueMemberS{Value: key.String()},
		},
	})
	if err != nil {
		return kowloon.IndexResultResponse{}, false, fmt.Errorf("dynamodb idempotency get: %w", err)
	}
	if out.Item == nil {
		return kowloon.IndexResultResponse{}, false, nil
	}
	respAttr, ok := out.Item["response"].(*types.AttributeValueMemberS)
	if !ok {
		return kowloon.IndexResultResponse{}, false, errors.New("dynamodb idempotency: missing response attribute")
	}
	var resp kowloon.IndexResultResponse
	if err := json.Unmarshal([]byte(respAttr.Value), &resp); err != nil {
		return kowloon.IndexResultResponse{}, false, fmt.Errorf("decode idempotency response: %w", err)
	}
	return resp, true, nil
}

func (s *Store) Save(ctx context.Context, key idempotency.Key, resp kowloon.IndexResultResponse) error {
	body, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("encode idempotency response: %w", err)
	}
	item := map[string]types.AttributeValue{
		"idempotency_key": &types.AttributeValueMemberS{Value: key.String()},
		"job_id":          &types.AttributeValueMemberS{Value: key.JobID},
		"result_uri":      &types.AttributeValueMemberS{Value: key.ResultURI},
		"model":           &types.AttributeValueMemberS{Value: key.Model},
		"dim":             &types.AttributeValueMemberN{Value: strconv.Itoa(key.Dimensions)},
		"response":        &types.AttributeValueMemberS{Value: string(body)},
	}
	if s.ttl > 0 {
		item["expires_at"] = &types.AttributeValueMemberN{
			Value: strconv.FormatInt(time.Now().Add(s.ttl).Unix(), 10),
		}
	}
	if _, err := s.client.PutItem(ctx, &awsdynamodb.PutItemInput{
		TableName: aws.String(s.tableName),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("dynamodb idempotency put: %w", err)
	}
	return nil
}
