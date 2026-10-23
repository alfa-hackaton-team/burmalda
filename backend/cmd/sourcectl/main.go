package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/database"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/sources"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := database.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	store := sources.NewStore(pool, sources.Validator{})

	var result any
	switch args[0] {
	case "create":
		if len(args) != 2 {
			return usage()
		}
		body, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		input, err := sources.ParseCreateJSON(body)
		if err != nil {
			return err
		}
		result, err = store.Create(ctx, input)
		if err != nil {
			return err
		}
	case "get":
		if len(args) != 2 {
			return usage()
		}
		result, err = store.Get(ctx, args[1])
		if err != nil {
			return err
		}
	case "list":
		if len(args) > 3 {
			return usage()
		}
		limit := 25
		if len(args) >= 2 {
			limit, err = strconv.Atoi(args[1])
			if err != nil {
				return err
			}
		}
		cursor := ""
		if len(args) == 3 {
			cursor = args[2]
		}
		result, err = store.List(ctx, limit, cursor)
		if err != nil {
			return err
		}
	case "patch":
		if len(args) != 4 {
			return usage()
		}
		version, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(args[3])
		if err != nil {
			return err
		}
		result, err = store.Update(ctx, args[1], version, body)
		if err != nil {
			return err
		}
	default:
		return usage()
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func usage() error {
	return fmt.Errorf("usage: sourcectl create FILE | get ID | list [LIMIT [CURSOR]] | patch ID CONFIG_VERSION FILE")
}
