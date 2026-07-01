package dynamodb

import (
	"context"
	"errors"
	"testing"

	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/keix/kowloon/internal/embed/cache"
)

type fakeClient struct {
	items    map[string]map[string]types.AttributeValue
	putCalls int
	getErr   error
	putErr   error
}

func newFakeClient() *fakeClient {
	return &fakeClient{items: map[string]map[string]types.AttributeValue{}}
}

func (f *fakeClient) GetItem(_ context.Context, in *awsdynamodb.GetItemInput, _ ...func(*awsdynamodb.Options)) (*awsdynamodb.GetItemOutput, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	k := in.Key["cache_key"].(*types.AttributeValueMemberS).Value
	item, ok := f.items[k]
	if !ok {
		return &awsdynamodb.GetItemOutput{}, nil
	}
	return &awsdynamodb.GetItemOutput{Item: item}, nil
}

func (f *fakeClient) PutItem(_ context.Context, in *awsdynamodb.PutItemInput, _ ...func(*awsdynamodb.Options)) (*awsdynamodb.PutItemOutput, error) {
	f.putCalls++
	if f.putErr != nil {
		return nil, f.putErr
	}
	k := in.Item["cache_key"].(*types.AttributeValueMemberS).Value
	f.items[k] = in.Item
	return &awsdynamodb.PutItemOutput{}, nil
}

func TestGet_Miss(t *testing.T) {
	c := New(Config{Client: newFakeClient(), TableName: "T"})
	k := cache.MakeKey("m", 4, "hello")
	_, ok, err := c.Get(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("want miss on empty table")
	}
}

func TestPut_Get_Roundtrip(t *testing.T) {
	fc := newFakeClient()
	c := New(Config{Client: fc, TableName: "T"})
	ctx := context.Background()

	k := cache.MakeKey("m", 4, "hello")
	want := []float32{1.5, -2.25, 3.75, 0}

	if err := c.Put(ctx, k, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.Get(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("want hit after Put")
	}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d", len(got), len(want))
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("[%d]=%v, want %v (bit-exact float32 required)", i, got[i], v)
		}
	}
}

func TestPut_TTLAttributePresent(t *testing.T) {
	fc := newFakeClient()
	c := New(Config{Client: fc, TableName: "T", TTL: 90 * 24 * 3600 * 1_000_000_000})
	ctx := context.Background()

	k := cache.MakeKey("m", 4, "x")
	if err := c.Put(ctx, k, []float32{1}); err != nil {
		t.Fatal(err)
	}
	item := fc.items[k.String()]
	if _, ok := item["expires_at"]; !ok {
		t.Error("want expires_at attribute when TTL configured")
	}
}

func TestPut_NoTTLWhenUnset(t *testing.T) {
	fc := newFakeClient()
	c := New(Config{Client: fc, TableName: "T"})
	ctx := context.Background()

	k := cache.MakeKey("m", 4, "x")
	if err := c.Put(ctx, k, []float32{1}); err != nil {
		t.Fatal(err)
	}
	item := fc.items[k.String()]
	if _, ok := item["expires_at"]; ok {
		t.Error("expires_at should be absent when TTL=0")
	}
}

func TestGet_ErrorPropagates(t *testing.T) {
	fc := newFakeClient()
	fc.getErr = errors.New("throttled")
	c := New(Config{Client: fc, TableName: "T"})

	_, _, err := c.Get(context.Background(), cache.MakeKey("m", 4, "x"))
	if err == nil {
		t.Fatal("want error")
	}
}

func TestPut_ErrorPropagates(t *testing.T) {
	fc := newFakeClient()
	fc.putErr = errors.New("throttled")
	c := New(Config{Client: fc, TableName: "T"})

	err := c.Put(context.Background(), cache.MakeKey("m", 4, "x"), []float32{1})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestEncodeDecode_LargeVector(t *testing.T) {
	v := make([]float32, 3072)
	for i := range v {
		v[i] = float32(i) * 0.001
	}
	got := decodeVector(encodeVector(v))
	if len(got) != len(v) {
		t.Fatalf("len=%d", len(got))
	}
	for i := range v {
		if got[i] != v[i] {
			t.Errorf("[%d]=%v, want %v", i, got[i], v[i])
			break
		}
	}
}
