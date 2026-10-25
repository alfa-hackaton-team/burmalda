package rawartifacts

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/observability"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type instrumentedS3 struct{ s3Client }

func measureS3[T any](operation string, call func() (T, error)) (result T, err error) {
	started := time.Now()
	defer func() { observability.S3Operation(operation, started, err) }()
	return call()
}

func (c instrumentedS3) PutObject(ctx context.Context, in *s3.PutObjectInput, options ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	out, err := measureS3("put", func() (*s3.PutObjectOutput, error) { return c.s3Client.PutObject(ctx, in, options...) })
	if err == nil {
		observability.S3Bytes("upload", aws.ToInt64(in.ContentLength))
	}
	return out, err
}
func (c instrumentedS3) HeadObject(ctx context.Context, in *s3.HeadObjectInput, options ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return measureS3("head", func() (*s3.HeadObjectOutput, error) { return c.s3Client.HeadObject(ctx, in, options...) })
}
func (c instrumentedS3) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, options ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return measureS3("list", func() (*s3.ListObjectsV2Output, error) { return c.s3Client.ListObjectsV2(ctx, in, options...) })
}
func (c instrumentedS3) HeadBucket(ctx context.Context, in *s3.HeadBucketInput, options ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return measureS3("head_bucket", func() (*s3.HeadBucketOutput, error) { return c.s3Client.HeadBucket(ctx, in, options...) })
}
func (c instrumentedS3) CreateBucket(ctx context.Context, in *s3.CreateBucketInput, options ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	return measureS3("create_bucket", func() (*s3.CreateBucketOutput, error) { return c.s3Client.CreateBucket(ctx, in, options...) })
}
func (c instrumentedS3) GetObject(ctx context.Context, in *s3.GetObjectInput, options ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	started := time.Now()
	out, err := c.s3Client.GetObject(ctx, in, options...)
	if err != nil {
		observability.S3Operation("get", started, err)
		return out, err
	}
	out.Body = &measuredBody{ReadCloser: out.Body, started: started}
	return out, nil
}

type measuredBody struct {
	io.ReadCloser
	started time.Time
	failed  atomic.Bool
	once    sync.Once
}

func (b *measuredBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	observability.S3Bytes("download", int64(n))
	if err != nil && err != io.EOF {
		b.failed.Store(true)
	}
	return n, err
}
func (b *measuredBody) Close() (err error) {
	b.once.Do(func() {
		err = b.ReadCloser.Close()
		if err != nil {
			b.failed.Store(true)
		}
		metricErr := err
		if b.failed.Load() && metricErr == nil {
			metricErr = io.ErrUnexpectedEOF
		}
		observability.S3Operation("get", b.started, metricErr)
	})
	return err
}
