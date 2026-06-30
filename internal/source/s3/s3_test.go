package s3

import "testing"

func TestParseURI(t *testing.T) {
	tests := []struct {
		uri    string
		bucket string
		key    string
		err    bool
	}{
		{"s3://b/k", "b", "k", false},
		{"s3://lady-glass-keix/results/transactions/tenant=keix/year=2026/month=06/smbc.json",
			"lady-glass-keix", "results/transactions/tenant=keix/year=2026/month=06/smbc.json", false},
		{"http://wrong", "", "", true},
		{"s3://", "", "", true},
		{"s3://b/", "", "", true},
		{"s3:///k", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.uri, func(t *testing.T) {
			b, k, err := parseURI(tc.uri)
			if (err != nil) != tc.err {
				t.Fatalf("err=%v, want err=%v", err, tc.err)
			}
			if tc.err {
				return
			}
			if b != tc.bucket || k != tc.key {
				t.Errorf("(%q,%q), want (%q,%q)", b, k, tc.bucket, tc.key)
			}
		})
	}
}
