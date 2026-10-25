package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/database"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/mljobs"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mlctl enqueue [flags] | audit-legacy | export [flags]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	pipeline := flags.String("pipeline-version", cfg.MLPipelineVersion, "ML-confirmed pipeline version")
	source := flags.String("source-id", "", "only this source UUID")
	version := flags.String("article-version-id", "", "only this article version UUID")
	after := flags.String("after", "", "continue after version UUID")
	defaultLimit := 1000
	if args[0] == "export" {
		defaultLimit = 20
	}
	limit := flags.Int("limit", defaultLimit, "batch size (1..10000)")
	all := flags.Bool("all-versions", false, "include historical versions; default: latest only")
	dry := flags.Bool("dry-run", false, "show candidates without creating jobs")
	output := flags.String("output", "", "export directory")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := database.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	store := mljobs.NewStore(db)
	var result any
	switch args[0] {
	case "enqueue":
		result, err = store.Enqueue(ctx, mljobs.EnqueueOptions{PipelineVersion: *pipeline, SourceID: *source, VersionID: *version, After: *after, Limit: *limit, MaxTextBytes: cfg.MLMaxTextBytes, AllVersions: *all, DryRun: *dry})
	case "audit-legacy":
		result, err = store.AuditLegacy(ctx)
	case "export":
		result, err = store.Export(ctx, mljobs.ExportOptions{PipelineVersion: *pipeline, SourceID: *source, VersionID: *version, Limit: *limit, Directory: *output, MaxTextBytes: cfg.MLMaxTextBytes})
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
