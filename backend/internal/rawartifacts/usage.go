package rawartifacts

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type BucketUsage struct {
	Bytes, Objects int64
}

func (s *S3) Usage(ctx context.Context) (BucketUsage, error) {
	var usage BucketUsage
	var token *string
	for {
		requestCtx, cancel := context.WithTimeout(ctx, time.Duration(s.timeout.Load()))
		page, err := s.client.ListObjectsV2(requestCtx, &s3.ListObjectsV2Input{
			Bucket: aws.String(s.bucket), ContinuationToken: token,
		})
		cancel()
		if err != nil {
			return BucketUsage{}, fmt.Errorf("inventory S3 bucket: %w", err)
		}
		for _, object := range page.Contents {
			if object.Key == nil || object.Size == nil || *object.Size < 0 || *object.Size > math.MaxInt64-usage.Bytes {
				return BucketUsage{}, fmt.Errorf("S3 returned an invalid object size or key")
			}
			usage.Bytes += *object.Size
			usage.Objects++
		}
		if !aws.ToBool(page.IsTruncated) {
			return usage, nil
		}
		if aws.ToString(page.NextContinuationToken) == "" || aws.ToString(page.NextContinuationToken) == aws.ToString(token) {
			return BucketUsage{}, fmt.Errorf("S3 returned an invalid inventory continuation token")
		}
		token = page.NextContinuationToken
	}
}
