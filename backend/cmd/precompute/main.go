// Explicit operator command. This executable belongs in the model worker
// environment (Python dependencies/cache/credentials), never the Go read API.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/database"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/userapi"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	user := flag.String("user-id", "", "existing authorized user UUID")
	cid := flag.String("context-id", "", "existing business context UUID")
	eid := flag.String("event-id", "", "existing indexed event UUID")
	set := flag.String("set-version", "", "frozen demo set version")
	python := flag.String("python", "python3", "Python executable in production ML environment")
	flag.Parse()
	if *user == "" || *cid == "" || *eid == "" || *set == "" {
		return fmt.Errorf("--user-id --context-id --event-id --set-version are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cfg, e := config.Load()
	if e != nil {
		return e
	}
	pool, e := database.Open(ctx, cfg)
	if e != nil {
		return e
	}
	defer pool.Close()
	store := userapi.NewPostgresStore(pool)
	store.ConfigureML("", "", os.Getenv("ML_ANALYSIS_CONFIG_VERSION"))
	store.ConfigureDemo(*set)
	if dsn := os.Getenv("ML_READ_DATABASE_URL"); dsn != "" {
		readCfg := cfg
		readCfg.DatabaseURL = dsn
		p, e := database.Open(ctx, readCfg)
		if e != nil {
			return e
		}
		defer p.Close()
		store.SetCorpusPool(p)
	}
	manifestBytes, e := exec.CommandContext(ctx, *python, "-m", "ml_runtime.precompute", "--manifest").Output()
	if e != nil {
		return fmt.Errorf("cannot inspect production ML runtime: %w", e)
	}
	var manifest map[string]any
	if e = json.Unmarshal(manifestBytes, &manifest); e != nil {
		return e
	}
	result, e := store.Precompute(ctx, *user, *cid, *eid, *set, manifest, func(ctx context.Context, input map[string]any) (map[string]any, error) {
		b, e := json.Marshal(input)
		if e != nil {
			return nil, e
		}
		cmd := exec.CommandContext(ctx, *python, "-m", "ml_runtime.precompute")
		cmd.Stdin = bytes.NewReader(b)
		// Bound stdout; never echo model/provider stderr, credentials or reasoning.
		var out bytes.Buffer
		cmd.Stdout = &limitedWriter{w: &out, left: 16 << 20}
		if e = cmd.Run(); e != nil {
			return nil, fmt.Errorf("production analysis process failed: %w", e)
		}
		var result map[string]any
		e = json.Unmarshal(out.Bytes(), &result)
		return result, e
	})
	if e != nil {
		return e
	}
	if e = json.NewEncoder(os.Stdout).Encode(result); e != nil {
		return e
	}
	if result["state"] != "SUCCEEDED" {
		return fmt.Errorf("demo is not ready; inspect recorded state (no automatic retry)")
	}
	return nil
}

type limitedWriter struct {
	w    io.Writer
	left int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > w.left {
		return 0, fmt.Errorf("analysis output exceeds size limit")
	}
	n, e := w.w.Write(p)
	w.left -= n
	return n, e
}
