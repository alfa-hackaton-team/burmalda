package rawartifacts

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/observability"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/prometheus/client_golang/prometheus"
)

func ioMetric(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	r := prometheus.NewRegistry()
	r.MustRegister(observability.IOCollectors()...)
	families, err := r.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			matched := 0
			for _, label := range metric.Label {
				if labels[label.GetName()] == label.GetValue() {
					matched++
				}
			}
			if matched == len(labels) {
				return metric.Counter.GetValue()
			}
		}
	}
	return 0
}

func TestS3GetMetricIncludesBodyAndRecordsOnlyOnce(t *testing.T) {
	labels := map[string]string{"operation": "get", "outcome": "success"}
	before := ioMetric(t, "ingestion_s3_operations_total", labels)
	bytesBefore := ioMetric(t, "ingestion_s3_transferred_bytes_total", map[string]string{"direction": "download"})
	client := instrumentedS3{&fakeS3Client{objects: map[string][]byte{"raw/a": []byte("payload")}}}
	out, err := client.GetObject(context.Background(), &s3.GetObjectInput{Key: aws.String("raw/a")})
	if err != nil {
		t.Fatal(err)
	}
	if ioMetric(t, "ingestion_s3_operations_total", labels) != before {
		t.Fatal("GET ended before body consumption")
	}
	if _, err := io.Copy(io.Discard, out.Body); err != nil {
		t.Fatal(err)
	}
	if err := out.Body.Close(); err != nil {
		t.Fatal(err)
	}
	_ = out.Body.Close()
	if ioMetric(t, "ingestion_s3_operations_total", labels) != before+1 {
		t.Fatal("GET was not counted exactly once")
	}
	if ioMetric(t, "ingestion_s3_transferred_bytes_total", map[string]string{"direction": "download"}) != bytesBefore+7 {
		t.Fatal("body bytes were not counted")
	}
}

type failingBody struct{}

func (failingBody) Read(p []byte) (int, error) { copy(p, "bad"); return 3, io.ErrUnexpectedEOF }
func (failingBody) Close() error               { return nil }

func TestS3BodyReadErrorIsNotSuccess(t *testing.T) {
	labels := map[string]string{"operation": "get", "outcome": "error"}
	before := ioMetric(t, "ingestion_s3_operations_total", labels)
	body := &measuredBody{ReadCloser: failingBody{}, started: time.Now()}
	if _, err := body.Read(make([]byte, 10)); err == nil {
		t.Fatal("read error lost")
	}
	_ = body.Close()
	if ioMetric(t, "ingestion_s3_operations_total", labels) != before+1 {
		t.Fatal("body error reported as success")
	}
}

func TestFailedS3PutDoesNotCountUploadedPayload(t *testing.T) {
	before := ioMetric(t, "ingestion_s3_transferred_bytes_total", map[string]string{"direction": "upload"})
	client := instrumentedS3{&fakeS3Client{objects: map[string][]byte{}}}
	_, err := client.PutObject(context.Background(), &s3.PutObjectInput{Key: aws.String("raw/a"), ContentLength: aws.Int64(7), Body: strings.NewReader("payload")})
	if err == nil {
		t.Fatal("fixture must reject missing conditional checksum")
	}
	if ioMetric(t, "ingestion_s3_transferred_bytes_total", map[string]string{"direction": "upload"}) != before {
		t.Fatal("failed upload counted as successful bytes")
	}
}
