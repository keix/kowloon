package cache

import "testing"

func TestMakeKey_StableForSameInputs(t *testing.T) {
	a := MakeKey("m", 128, "hello")
	b := MakeKey("m", 128, "hello")
	if a != b {
		t.Errorf("keys differ: %v vs %v", a, b)
	}
}

func TestMakeKey_DifferentModelDiffers(t *testing.T) {
	a := MakeKey("m1", 128, "hello")
	b := MakeKey("m2", 128, "hello")
	if a == b {
		t.Error("keys should differ by model")
	}
}

func TestMakeKey_DifferentDimDiffers(t *testing.T) {
	a := MakeKey("m", 3072, "hello")
	b := MakeKey("m", 1536, "hello")
	if a == b {
		t.Error("keys should differ by dimensions")
	}
}

func TestMakeKey_DifferentTextDiffers(t *testing.T) {
	a := MakeKey("m", 128, "hello")
	b := MakeKey("m", 128, "world")
	if a == b {
		t.Error("keys should differ by text")
	}
}

func TestKey_StringDeterministic(t *testing.T) {
	a := MakeKey("m", 128, "x")
	if a.String() != a.String() {
		t.Error("String() should be deterministic")
	}
}
