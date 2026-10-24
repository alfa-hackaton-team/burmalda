package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/database"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/dataset"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/rawartifacts"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	dir := os.Getenv("DATASET_DIR")
	if dir == "" {
		dir = "../data/hackathon"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := database.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	var store rawartifacts.Store = rawartifacts.PostgreSQL{}
	var s3 *rawartifacts.S3
	if cfg.RawStorageBackend == "s3" {
		s3, err = rawartifacts.NewS3(ctx, cfg)
		if err != nil {
			return err
		}
		store = s3
	}
	result, err := dataset.Ensure(ctx, db, dir, store, s3)
	if err != nil {
		return err
	}
	reports, err := dataset.EnsureBankReportDocuments(ctx, db, cfg.MLPipelineVersion, cfg.MLMaxTextBytes)
	if err != nil {
		return err
	}
	if result.Skipped {
		if result.Normalized {
			fmt.Printf("Dataset %s verified and normalized\n", result.BundleID)
		} else {
			fmt.Printf("Dataset %s already loaded and verified\n", result.BundleID)
		}
	} else {
		fmt.Printf("Dataset %s loaded: %v\n", result.BundleID, result.Rows)
	}
	fmt.Printf("Bank reports linked to article processing: %d\n", reports)
	return nil
}
