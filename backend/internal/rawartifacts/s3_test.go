package rawartifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
)

type fakeS3Client struct {
	objects map[string][]byte
	putErr  error
	puts    int
}

func (f *fakeS3Client) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.puts++
	if f.putErr != nil {
		return nil, f.putErr
	}
	if aws.ToString(input.IfNoneMatch) != "*" || aws.ToString(input.ChecksumSHA256) == "" {
		return nil, errors.New("conditional upload or checksum missing")
	}
	data, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	key := aws.ToString(input.Key)
	if _, ok := f.objects[key]; ok {
		return nil, errors.New("object exists")
	}
	f.objects[key] = data
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3Client) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	data, ok := f.objects[aws.ToString(input.Key)]
	if !ok {
		return nil, errors.New("missing object")
	}
	sum := sha256.Sum256(data)
	return &s3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(data))),
		Metadata: map[string]string{"sha256": hex.EncodeToString(sum[:])}}, nil
}

func (f *fakeS3Client) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	data, ok := f.objects[aws.ToString(input.Key)]
	if !ok {
		return nil, errors.New("missing object")
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(data))}, nil
}

func (f *fakeS3Client) ListObjectsV2(_ context.Context, _ *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return &s3.ListObjectsV2Output{Contents: []types.Object{}}, nil
}

func (f *fakeS3Client) HeadBucket(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return &s3.HeadBucketOutput{}, nil
}

func (f *fakeS3Client) CreateBucket(_ context.Context, _ *s3.CreateBucketInput, _ ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	return &s3.CreateBucketOutput{}, nil
}

func TestS3PrepareAndVerify(t *testing.T) {
	sourceID := uuid.NewString()
	client := &fakeS3Client{objects: make(map[string][]byte)}
	store := &S3{client: client, bucket: "private-originals"}
	store.SetRequestTimeout(time.Second)
	data := []byte("<article>example</article>")
	artifact, err := store.Prepare(context.Background(), sourceID, "text/html", data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(artifact.ObjectURI, "s3://private-originals/raw/"+sourceID+"/") || artifact.Size != int64(len(data)) || artifact.Content != nil {
		t.Fatalf("unexpected artifact: %+v", artifact)
	}
	if err := store.Verify(context.Background(), artifact); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Prepare(context.Background(), sourceID, "text/html", data); err != nil {
		t.Fatalf("existing identical object should be accepted: %v", err)
	}
	client.objects["raw/"+sourceID+"/"+artifact.SHA256] = []byte("corrupt")
	if _, err := store.Prepare(context.Background(), sourceID, "text/html", data); err == nil {
		t.Fatal("corrupt existing object was accepted")
	}
}

func TestS3LostUploadResponse(t *testing.T) {
	sourceID := uuid.NewString()
	data := []byte("content")
	sum := sha256.Sum256(data)
	key := "raw/" + sourceID + "/" + hex.EncodeToString(sum[:])
	client := &fakeS3Client{objects: map[string][]byte{key: data}, putErr: errors.New("connection reset")}
	store := &S3{client: client, bucket: "private-originals"}
	store.SetRequestTimeout(time.Second)
	if _, err := store.Prepare(context.Background(), sourceID, "text/html", data); err != nil {
		t.Fatalf("existing verified object should recover upload: %v", err)
	}
}

func TestS3ClientWithHTTPObjectStore(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "test-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret")
	objects := make(map[string][]byte)
	bucketExists := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSuffix(r.URL.Path, "/") == "/test-bucket" {
			switch r.Method {
			case http.MethodHead:
				if !bucketExists {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(http.StatusOK)
			case http.MethodPut:
				bucketExists = true
				w.WriteHeader(http.StatusOK)
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/test-bucket/raw/") {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/test-bucket/")
		switch r.Method {
		case http.MethodPut:
			if r.Header.Get("If-None-Match") != "*" || r.Header.Get("X-Amz-Checksum-Sha256") == "" {
				http.Error(w, "missing integrity headers", http.StatusBadRequest)
				return
			}
			if _, exists := objects[key]; exists {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			data, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			objects[key] = data
			w.WriteHeader(http.StatusOK)
		case http.MethodHead:
			data, exists := objects[key]
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			sum := sha256.Sum256(data)
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			w.Header().Set("X-Amz-Meta-Sha256", hex.EncodeToString(sum[:]))
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			data, exists := objects[key]
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(data)
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	runtime := config.DefaultRuntime()
	runtime.RawStorage.RequestTimeout = config.Duration(time.Second)
	store, err := NewS3(context.Background(), config.Config{RawStorageBackend: "s3", S3Bucket: "test-bucket",
		S3Region: "us-east-1", S3Endpoint: server.URL, Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureBucket(context.Background()); err != nil || !bucketExists {
		t.Fatalf("bucket initialization failed: %v", err)
	}
	sourceID := uuid.NewString()
	artifact, err := store.Prepare(context.Background(), sourceID, "text/html", []byte("sample"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Verify(context.Background(), artifact); err != nil {
		t.Fatal(err)
	}
}

func TestReadOriginalIsBoundedAndBucketScoped(t *testing.T) {
	data := []byte("%PDF-test")
	client := &fakeS3Client{objects: map[string][]byte{"raw/a/test": data}}
	store := &S3{client: client, bucket: "private"}
	store.timeout.Store(int64(time.Second))
	object := Artifact{ObjectURI: "s3://private/raw/a/test", Size: int64(len(data))}
	got, err := store.Read(context.Background(), object, 1024)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("%s %v", got, err)
	}
	if _, err = store.Read(context.Background(), object, 1); err == nil {
		t.Fatal("unbounded original")
	}
	object.Size--
	if _, err = store.Read(context.Background(), object, 1024); err == nil {
		t.Fatal("wrong size accepted")
	}
	object.ObjectURI = "s3://other/raw/a/test"
	if _, err = store.Read(context.Background(), object, 1024); err == nil {
		t.Fatal("outside bucket accepted")
	}
}
