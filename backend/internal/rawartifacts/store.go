package rawartifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

type Artifact struct {
	SHA256      string
	ContentType string
	Size        int64
	Content     []byte
	ObjectURI   string
}

type Store interface {
	Prepare(context.Context, string, string, []byte) (Artifact, error)
}

type PostgreSQL struct{}

func (PostgreSQL) Prepare(_ context.Context, _ string, contentType string, data []byte) (Artifact, error) {
	if contentType == "" {
		return Artifact{}, fmt.Errorf("raw artifact content type is empty")
	}
	sum := sha256.Sum256(data)
	return Artifact{SHA256: hex.EncodeToString(sum[:]), ContentType: contentType,
		Size: int64(len(data)), Content: data}, nil
}
