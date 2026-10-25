package rawartifacts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type inventoryClient struct {
	fakeS3Client
	list func(*s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error)
}

func (c *inventoryClient) ListObjectsV2(_ context.Context, input *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return c.list(input)
}

func TestBucketUsageIncludesEveryPageAndPrefix(t *testing.T) {
	calls := 0
	client := &inventoryClient{list: func(input *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
		calls++
		if aws.ToString(input.Bucket) != "originals" || input.Prefix != nil || input.Delimiter != nil {
			t.Fatalf("inventory must cover the entire bucket: %+v", input)
		}
		if calls == 1 {
			if input.ContinuationToken != nil {
				t.Fatal("unexpected initial cursor")
			}
			return &s3.ListObjectsV2Output{Contents: []types.Object{{Key: aws.String("raw/a"), Size: aws.Int64(10)}}, IsTruncated: aws.Bool(true), NextContinuationToken: aws.String("page2")}, nil
		}
		if aws.ToString(input.ContinuationToken) != "page2" {
			t.Fatal("cursor not passed")
		}
		return &s3.ListObjectsV2Output{Contents: []types.Object{{Key: aws.String("other/report"), Size: aws.Int64(25)}, {Key: aws.String("empty"), Size: aws.Int64(0)}}}, nil
	}}
	store := &S3{client: client, bucket: "originals"}
	store.SetRequestTimeout(time.Second)
	usage, err := store.Usage(context.Background())
	if err != nil || usage.Bytes != 35 || usage.Objects != 3 || calls != 2 {
		t.Fatalf("usage=%+v calls=%d err=%v", usage, calls, err)
	}
}

func TestBucketUsageRejectsIncompleteInventory(t *testing.T) {
	for _, name := range []string{"missing-size", "negative-size", "missing-cursor", "repeated-cursor", "page-error"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			client := &inventoryClient{list: func(input *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
				calls++
				page := &s3.ListObjectsV2Output{Contents: []types.Object{{Key: aws.String("raw/a"), Size: aws.Int64(10)}}}
				switch name {
				case "missing-size":
					page.Contents[0].Size = nil
				case "negative-size":
					page.Contents[0].Size = aws.Int64(-1)
				case "missing-cursor":
					page.IsTruncated = aws.Bool(true)
				case "repeated-cursor":
					page.IsTruncated, page.NextContinuationToken = aws.Bool(true), aws.String("same")
				case "page-error":
					if calls == 2 {
						return nil, errors.New("access denied")
					}
					page.IsTruncated, page.NextContinuationToken = aws.Bool(true), aws.String("page2")
				}
				return page, nil
			}}
			store := &S3{client: client, bucket: "originals"}
			store.SetRequestTimeout(time.Second)
			usage, err := store.Usage(context.Background())
			if err == nil || usage != (BucketUsage{}) {
				t.Fatalf("partial inventory accepted: %+v %v", usage, err)
			}
		})
	}
}
