package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
)

func main() {
	apply := flag.Bool("apply", false, "apply the migration; default is dry-run")
	confirmation := flag.String("confirm", "", "required apply confirmation")
	flag.Parse()

	if err := os.Setenv("SUB2API_OPENAI_PERSONA_MIGRATION", "1"); err != nil {
		fatal(err)
	}
	cfg, err := config.LoadForBootstrap()
	if err != nil {
		fatal(err)
	}
	client, db, err := repository.InitEnt(cfg)
	if err != nil {
		fatal(err)
	}
	defer func() { _ = client.Close() }()
	encryptor, err := repository.NewAESEncryptor(cfg)
	if err != nil {
		fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	report, err := repository.RunOpenAIAccountPersonaMigration(ctx, db, encryptor, *apply, *confirmation)
	if err != nil {
		fatal(err)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fatal(err)
	}
	fmt.Println(string(encoded))
	if !report.Ready {
		os.Exit(2)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "openai-persona-migrate:", err)
	os.Exit(1)
}
