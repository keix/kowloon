package dynamodb

import (
	"context"
	"errors"
	"testing"
	"time"

	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/keix/kowloon"
	"github.com/keix/kowloon/internal/idempotency"
)

type fakeClient struct {
	items  map[string]map[string]types.AttributeValue
	getErr error
	putErr error
}

func newFakeClient() *fakeClient {
	return &fakeClient{items: map[string]map[string]types.AttributeValue{}}
}

func (f *fakeClient) GetItem(_ context.Context, in *awsdynamodb.GetItemInput, _ ...func(*awsdynamodb.Options)) (*awsdynamodb.GetItemOutput, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	k := in.Key["idempotency_key"].(*types.AttributeValueMemberS).Value
	item, ok := f.items[k]
	if !ok {
		return &awsdynamodb.GetItemOutput{}, nil
	}
	return &awsdynamodb.GetItemOutput{Item: item}, nil
}

func (f *fakeClient) PutItem(_ context.Context, in *awsdynamodb.PutItemInput, _ ...func(*awsdynamodb.Options)) (*awsdynamodb.PutItemOutput, error) {
	if f.putErr != nil {
		return nil, f.putErr
	}
	k := in.Item["idempotency_key"].(*types.AttributeValueMemberS).Value
	f.items[k] = in.Item
	return &awsdynamodb.PutItemOutput{}, nil
}

func req() kowloon.IndexResultRequest {
	return kowloon.IndexResultRequest{
		JobID:         "j",
		ResultURI:     "s3://b/k",
		SchemaVersion: "transactions.v1",
	}
}

func resp() kowloon.IndexResultResponse {
	return kowloon.IndexResultResponse{
		Status:            "indexed",
		KowloonCollection: "transactions",
		IndexJobID:        "kidx_42",
		VectorCount:       33,
		EmbeddingModel:    "text-embedding-3-large",
		IndexedAt:         time.Unix(1_700_000_000, 0).UTC(),
	}
}

func TestLookup_Miss(t *testing.T) {
	s := New(Config{Client: newFakeClient(), TableName: "T"})
	k := idempotency.MakeKey(req(), "m", 128, []byte("x"))
	_, ok, err := s.Lookup(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("want miss on empty table")
	}
}

func TestSave_Lookup_Roundtrip(t *testing.T) {
	fc := newFakeClient()
	s := New(Config{Client: fc, TableName: "T"})
	ctx := context.Background()

	k := idempotency.MakeKey(req(), "m", 128, []byte("x"))
	want := resp()

	if err := s.Save(ctx, k, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Lookup(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("want hit after Save")
	}
	if got.IndexJobID != want.IndexJobID || got.VectorCount != want.VectorCount || got.EmbeddingModel != want.EmbeddingModel {
		t.Errorf("got=%+v, want=%+v", got, want)
	}
	if !got.IndexedAt.Equal(want.IndexedAt) {
		t.Errorf("IndexedAt=%v, want %v", got.IndexedAt, want.IndexedAt)
	}
}

func TestSave_TTLAttributePresent(t *testing.T) {
	fc := newFakeClient()
	s := New(Config{Client: fc, TableName: "T", TTL: 30 * 24 * time.Hour})
	ctx := context.Background()

	k := idempotency.MakeKey(req(), "m", 128, []byte("x"))
	if err := s.Save(ctx, k, resp()); err != nil {
		t.Fatal(err)
	}
	item := fc.items[k.String()]
	if _, ok := item["expires_at"]; !ok {
		t.Error("want expires_at attribute when TTL configured")
	}
}

func TestSave_NoTTLWhenUnset(t *testing.T) {
	fc := newFakeClient()
	s := New(Config{Client: fc, TableName: "T"})
	ctx := context.Background()

	k := idempotency.MakeKey(req(), "m", 128, []byte("x"))
	if err := s.Save(ctx, k, resp()); err != nil {
		t.Fatal(err)
	}
	item := fc.items[k.String()]
	if _, ok := item["expires_at"]; ok {
		t.Error("expires_at should be absent when TTL=0")
	}
}

func TestLookup_ErrorPropagates(t *testing.T) {
	fc := newFakeClient()
	fc.getErr = errors.New("throttled")
	s := New(Config{Client: fc, TableName: "T"})

	_, _, err := s.Lookup(context.Background(), idempotency.MakeKey(req(), "m", 128, []byte("x")))
	if err == nil {
		t.Fatal("want error")
	}
}

func TestSave_ErrorPropagates(t *testing.T) {
	fc := newFakeClient()
	fc.putErr = errors.New("throttled")
	s := New(Config{Client: fc, TableName: "T"})

	err := s.Save(context.Background(), idempotency.MakeKey(req(), "m", 128, []byte("x")), resp())
	if err == nil {
		t.Fatal("want error")
	}
}
