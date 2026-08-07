package objectstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type S3 struct {
	client *s3.Client
	bucket string
	prefix string
}

const maxObjectSize int64 = 32 << 20

func OpenS3(ctx context.Context, bucket, prefix, region string) (*S3, error) {
	if strings.TrimSpace(bucket) == "" || strings.TrimSpace(region) == "" {
		return nil, errors.New("S3 bucket and region are required")
	}
	root, err := normalizePrefix(prefix)
	if err != nil {
		return nil, err
	}
	configuration, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	return &S3{client: s3.NewFromConfig(configuration), bucket: bucket, prefix: root}, nil
}

func (s *S3) Put(ctx context.Context, key string, content []byte, contentType string) error {
	if int64(len(content)) > maxObjectSize {
		return errors.New("object exceeds 32 MiB")
	}
	key, err := s.key(key)
	if err != nil {
		return err
	}
	input := &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(content),
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}
	if _, err := s.client.PutObject(ctx, input); err != nil {
		return fmt.Errorf("put S3 object %q: %w", key, err)
	}
	return nil
}

func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	key, err := s.key(key)
	if err != nil {
		return nil, err
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key),
	})
	if err != nil {
		var apiError smithy.APIError
		if errors.As(err, &apiError) && (apiError.ErrorCode() == "NoSuchKey" || apiError.ErrorCode() == "NotFound") {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get S3 object %q: %w", key, err)
	}
	defer result.Body.Close()
	content, err := io.ReadAll(io.LimitReader(result.Body, maxObjectSize+1))
	if err != nil {
		return nil, fmt.Errorf("read S3 object %q: %w", key, err)
	}
	if int64(len(content)) > maxObjectSize {
		return nil, fmt.Errorf("read S3 object %q: object exceeds 32 MiB", key)
	}
	return content, nil
}

func (s *S3) List(ctx context.Context, prefix string) ([]string, error) {
	logicalPrefix, err := normalizeKeyPrefix(prefix)
	if err != nil {
		return nil, err
	}
	fullPrefix := s.prefix + logicalPrefix
	var keys []string
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket), Prefix: aws.String(fullPrefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list S3 objects %q: %w", fullPrefix, err)
		}
		for _, object := range page.Contents {
			key := strings.TrimPrefix(aws.ToString(object.Key), s.prefix)
			if key != "" {
				keys = append(keys, key)
			}
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (s *S3) key(key string) (string, error) {
	logical, err := normalizeKey(key)
	if err != nil {
		return "", err
	}
	return s.prefix + logical, nil
}

func normalizePrefix(prefix string) (string, error) {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return "", nil
	}
	key, err := normalizeKey(prefix)
	if err != nil {
		return "", fmt.Errorf("invalid S3 prefix: %w", err)
	}
	return key + "/", nil
}

func normalizeKeyPrefix(prefix string) (string, error) {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return "", nil
	}
	key, err := normalizeKey(prefix)
	if err != nil {
		return "", err
	}
	return key + "/", nil
}

func normalizeKey(key string) (string, error) {
	key = strings.TrimSpace(strings.Trim(key, "/"))
	if key == "" || key == "." || strings.ContainsAny(key, "\\\x00\r\n") || path.Clean(key) != key || strings.HasPrefix(key, "../") {
		return "", errors.New("object key is invalid")
	}
	return key, nil
}
