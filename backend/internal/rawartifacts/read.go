package rawartifacts

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Read returns bounded bytes; the API checks their persisted hash before serving.
func (s *S3) Read(ctx context.Context, object Artifact, maxBytes int64) ([]byte, error) {
	if object.Size < 0 || object.Size > maxBytes {
		return nil, fmt.Errorf("original exceeds download limit")
	}
	key, err := s.key(object.ObjectURI)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(s.timeout.Load()))
	defer cancel()
	output, err := s.client.GetObject(requestCtx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	defer output.Body.Close()
	data, err := io.ReadAll(io.LimitReader(output.Body, object.Size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != object.Size {
		return nil, fmt.Errorf("original size differs from persisted artifact")
	}
	return data, nil
}
