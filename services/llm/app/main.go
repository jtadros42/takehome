package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	llm "github.com/jtadros42/takehome/services/llm"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	env, err := llm.NewConfigFromEnv()
	if err != nil {
		slog.Error("loading env config", "error", err)
		os.Exit(1)
	}

	repo := llm.NewInMemoryJobRepository().
		WithJobTemplates(
			[]llm.EvertuneJobTemplate{
				{
					TemplateID:     "luxury-cars-us",
					Name:           "Luxury Car Brands (US)",
					Description:    "Track how each model ranks luxury car brands in the US.",
					Category:       "luxury car brands",
					Region:         "US",
					TopN:           10,
					SamplesPerCell: 10,
					ModelProviders: []llm.ModelProvider{
						{
							Provider: llm.ProviderVertexAI,
							Model:    llm.GeminiFlash,
						},
					},
				},
			},
		)

	svc, err := llm.NewService(ctx, env, repo)
	if err != nil {
		slog.Error("creating service", "error", err)
		os.Exit(1)
	}

	if err := svc.Run(ctx); err != nil {
		slog.Error("service exited", "error", err)
		os.Exit(1)
	}
	slog.Info("service stopped")
}
