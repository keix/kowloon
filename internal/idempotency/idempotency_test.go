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
	a := MakeKey(req(), "rev", "m", 128, []byte("hello"))
	b := MakeKey(req(), "rev", "m", 128, []byte("hello"))
	if a != b {
		t.Errorf("keys differ: %v vs %v", a, b)
	}
}

func TestMakeKey_DifferentContentDiffers(t *testing.T) {
	a := MakeKey(req(), "rev", "m", 128, []byte("hello"))
	b := MakeKey(req(), "rev", "m", 128, []byte("HELLO"))
	if a == b {
		t.Error("keys should differ by content")
	}
}

func TestMakeKey_DifferentModelDiffers(t *testing.T) {
	a := MakeKey(req(), "rev", "m1", 128, []byte("x"))
	b := MakeKey(req(), "rev", "m2", 128, []byte("x"))
	if a == b {
		t.Error("keys should differ by model")
	}
}

func TestMakeKey_DifferentDimDiffers(t *testing.T) {
	a := MakeKey(req(), "rev", "m", 3072, []byte("x"))
	b := MakeKey(req(), "rev", "m", 1536, []byte("x"))
	if a == b {
		t.Error("keys should differ by dimensions")
	}
}

// A converter revision bump must change the key: same input bytes, same
// model, but different output vectors. This is what lets a re-index run
// after the converter's embed-text logic changes instead of being
// skipped as a duplicate.
func TestMakeKey_DifferentConverterRevisionDiffers(t *testing.T) {
	a := MakeKey(req(), "1", "m", 128, []byte("x"))
	b := MakeKey(req(), "2", "m", 128, []byte("x"))
	if a == b {
		t.Error("keys should differ by converter revision")
	}
}

func TestMakeKey_DifferentJobIDDiffers(t *testing.T) {
	r1 := req()
	r2 := req()
	r2.JobID = "job_2"
	if MakeKey(r1, "rev", "m", 128, []byte("x")) == MakeKey(r2, "rev", "m", 128, []byte("x")) {
		t.Error("keys should differ by job_id")
	}
}

func TestMakeKey_DifferentSchemaVersionDiffers(t *testing.T) {
	r1 := req()
	r2 := req()
	r2.SchemaVersion = "transactions.v2"
	if MakeKey(r1, "rev", "m", 128, []byte("x")) == MakeKey(r2, "rev", "m", 128, []byte("x")) {
		t.Error("keys should differ by schema_version")
	}
}

func TestKey_StringDeterministic(t *testing.T) {
	k := MakeKey(req(), "rev", "m", 128, []byte("x"))
	if k.String() != k.String() {
		t.Error("String() should be deterministic")
	}
}
