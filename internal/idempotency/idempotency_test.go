package idempotency

import (
	"testing"

	"github.com/keix/kowloon"
)

func req() kowloon.IndexResultRequest {
	return kowloon.IndexResultRequest{
		JobID:         "job_1",
		TenantID:      "keix",
		ResultURI:     "s3://b/k.json",
		ResultType:    kowloon.ResultTypeTransactions,
		SchemaVersion: "transactions.v1",
	}
}

func TestMakeKey_StableForSameInputs(t *testing.T) {
	a := MakeKey(req(), "m", 128, []byte("hello"))
	b := MakeKey(req(), "m", 128, []byte("hello"))
	if a != b {
		t.Errorf("keys differ: %v vs %v", a, b)
	}
}

func TestMakeKey_DifferentContentDiffers(t *testing.T) {
	a := MakeKey(req(), "m", 128, []byte("hello"))
	b := MakeKey(req(), "m", 128, []byte("HELLO"))
	if a == b {
		t.Error("keys should differ by content")
	}
}

func TestMakeKey_DifferentModelDiffers(t *testing.T) {
	a := MakeKey(req(), "m1", 128, []byte("x"))
	b := MakeKey(req(), "m2", 128, []byte("x"))
	if a == b {
		t.Error("keys should differ by model")
	}
}

func TestMakeKey_DifferentDimDiffers(t *testing.T) {
	a := MakeKey(req(), "m", 3072, []byte("x"))
	b := MakeKey(req(), "m", 1536, []byte("x"))
	if a == b {
		t.Error("keys should differ by dimensions")
	}
}

func TestMakeKey_DifferentJobIDDiffers(t *testing.T) {
	r1 := req()
	r2 := req()
	r2.JobID = "job_2"
	if MakeKey(r1, "m", 128, []byte("x")) == MakeKey(r2, "m", 128, []byte("x")) {
		t.Error("keys should differ by job_id")
	}
}

func TestMakeKey_DifferentSchemaVersionDiffers(t *testing.T) {
	r1 := req()
	r2 := req()
	r2.SchemaVersion = "transactions.v2"
	if MakeKey(r1, "m", 128, []byte("x")) == MakeKey(r2, "m", 128, []byte("x")) {
		t.Error("keys should differ by schema_version")
	}
}

func TestKey_StringDeterministic(t *testing.T) {
	k := MakeKey(req(), "m", 128, []byte("x"))
	if k.String() != k.String() {
		t.Error("String() should be deterministic")
	}
}
