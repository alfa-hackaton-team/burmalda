package rawartifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/google/uuid"
)

type s3Client interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	CreateBucket(context.Context, *s3.CreateBucketInput, ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
}

type Object struct {
	URI          string
	LastModified time.Time
}

type S3 struct {
	client  s3Client
	bucket  string
	timeout atomic.Int64
}

func NewS3(ctx context.Context, cfg config.Config) (*S3, error) {
	if cfg.RawStorageBackend != "s3" {
		return nil, fmt.Errorf("S3 storage is not configured")
	}
	if cfg.Runtime.RawStorage.RequestTimeout <= 0 {
		return nil, fmt.Errorf("raw storage request timeout must be positive")
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.S3Region))
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		if cfg.S3Endpoint != "" {
			options.BaseEndpoint = aws.String(cfg.S3Endpoint)
			options.UsePathStyle = true
		}
	})
	store := &S3{client: instrumentedS3{client}, bucket: cfg.S3Bucket}
	store.SetRequestTimeout(cfg.Runtime.RawStorage.RequestTimeout.Std())
	return store, nil
}

func (s *S3) SetRequestTimeout(value time.Duration) {
	if value > 0 {
		s.timeout.Store(int64(value))
	}
}

func (s *S3) EnsureBucket(ctx context.Context) error {
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(s.timeout.Load()))
	_, err := s.client.HeadBucket(requestCtx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	cancel()
	if err == nil {
		return nil
	}
	var responseError *smithyhttp.ResponseError
	if !errors.As(err, &responseError) || responseError.HTTPStatusCode() != http.StatusNotFound {
		return fmt.Errorf("check S3 bucket: %w", err)
	}
	requestCtx, cancel = context.WithTimeout(ctx, time.Duration(s.timeout.Load()))
	defer cancel()
	if _, err := s.client.CreateBucket(requestCtx, &s3.CreateBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
		return fmt.Errorf("create S3 bucket: %w", err)
	}
	return nil
}

func (s *S3) Prepare(ctx context.Context, sourceID, contentType string, data []byte) (Artifact, error) {
	if _, err := uuid.Parse(sourceID); err != nil {
		return Artifact{}, fmt.Errorf("invalid source ID: %w", err)
	}
	if contentType == "" {
		return Artifact{}, fmt.Errorf("raw artifact content type is empty")
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	key := "raw/" + sourceID + "/" + hash
	object := Artifact{SHA256: hash, ContentType: contentType,
		Size: int64(len(data)), ObjectURI: "s3://" + s.bucket + "/" + key}
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(s.timeout.Load()))
	defer cancel()
	_, putErr := s.client.PutObject(requestCtx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(data),
		ContentLength: aws.Int64(object.Size), ContentType: aws.String(contentType),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum[:])),
		Metadata:       map[string]string{"sha256": hash}, IfNoneMatch: aws.String("*"),
	})
	if putErr != nil {
		if err := s.Verify(ctx, object); err == nil {
			return object, nil
		}
		return Artifact{}, fmt.Errorf("upload raw artifact: %w", putErr)
	}
	if err := s.checkHead(ctx, object); err != nil {
		return Artifact{}, fmt.Errorf("verify uploaded raw artifact: %w", err)
	}
	return object, nil
}

func (s *S3) checkHead(ctx context.Context, object Artifact) error {
	key, err := s.key(object.ObjectURI)
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(s.timeout.Load()))
	defer cancel()
	head, err := s.client.HeadObject(requestCtx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key),
	})
	if err != nil {
		return err
	}
	if aws.ToInt64(head.ContentLength) != object.Size || head.Metadata["sha256"] != object.SHA256 {
		return fmt.Errorf("S3 object size or SHA-256 metadata differs from uploaded bytes")
	}
	return nil
}

func (s *S3) VerifyMetadata(ctx context.Context, object Artifact) error {
	return s.checkHead(ctx, object)
}

func (s *S3) Verify(ctx context.Context, object Artifact) error {
	key, err := s.key(object.ObjectURI)
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(s.timeout.Load()))
	defer cancel()
	output, err := s.client.GetObject(requestCtx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key),
	})
	if err != nil {
		return err
	}
	defer output.Body.Close()
	if object.Size < 0 {
		return errors.New("negative artifact size")
	}
	hasher := sha256.New()
	n, err := io.Copy(hasher, io.LimitReader(output.Body, object.Size+1))
	if err != nil {
		return err
	}
	if n != object.Size || hex.EncodeToString(hasher.Sum(nil)) != object.SHA256 {
		return fmt.Errorf("S3 object size or SHA-256 differs from database artifact")
	}
	return nil
}

func (s *S3) key(uri string) (string, error) {
	prefix := "s3://" + s.bucket + "/"
	if !strings.HasPrefix(uri, prefix) {
		return "", fmt.Errorf("S3 object URI is outside configured bucket")
	}
	key := strings.TrimPrefix(uri, prefix)
	if !strings.HasPrefix(key, "raw/") || strings.Contains(key, "..") {
		return "", fmt.Errorf("invalid raw artifact object key")
	}
	return key, nil
}

func (s *S3) List(ctx context.Context, visit func(Object) error) error {
	var token *string
	for {
		requestCtx, cancel := context.WithTimeout(ctx, time.Duration(s.timeout.Load()))
		page, err := s.client.ListObjectsV2(requestCtx, &s3.ListObjectsV2Input{
			Bucket: aws.String(s.bucket), Prefix: aws.String("raw/"), ContinuationToken: token,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("list raw artifacts: %w", err)
		}
		for _, item := range page.Contents {
			if item.Key == nil || item.LastModified == nil {
				return fmt.Errorf("S3 returned an object without key or modification time")
			}
			if err := visit(Object{URI: "s3://" + s.bucket + "/" + aws.ToString(item.Key),
				LastModified: aws.ToTime(item.LastModified)}); err != nil {
				return err
			}
		}
		if !aws.ToBool(page.IsTruncated) {
			return nil
		}
		if page.NextContinuationToken == nil || aws.ToString(page.NextContinuationToken) == "" {
			return fmt.Errorf("S3 returned a truncated listing without a continuation token")
		}
		token = page.NextContinuationToken
	}
}

var _ Store = PostgreSQL{}
var _ Store = (*S3)(nil)
var _ s3Client = (*s3.Client)(nil)
