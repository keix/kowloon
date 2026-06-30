// Package s3 implements source.Source against AWS S3. The Lady Glass
// archive-result stage writes normalized results to a permanent bucket
// and hands the resulting `s3://bucket/key` URI to Kowloon; this is
// the reader that fetches the bytes.
package s3

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

// Client is the subset of *awss3.Client the reader needs. Splitting it
// out keeps the source easy to fake in unit tests without spinning up
// LocalStack.
type Client interface {
	GetObject(ctx context.Context, params *awss3.GetObjectInput, optFns ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
}

type Source struct {
	client Client
}

func New(client Client) *Source {
	return &Source{client: client}
}

func (s *Source) Read(ctx context.Context, uri string) ([]byte, error) {
	bucket, key, err := parseURI(uri)
	if err != nil {
		return nil, err
	}
	out, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 get %s: %w", uri, err)
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func parseURI(uri string) (string, string, error) {
	if !strings.HasPrefix(uri, "s3://") {
		return "", "", fmt.Errorf("not an s3 URI: %s", uri)
	}
	parts := strings.SplitN(strings.TrimPrefix(uri, "s3://"), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("malformed s3 URI: %s", uri)
	}
	return parts[0], parts[1], nil
}
