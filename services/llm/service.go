package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"golang.org/x/sync/errgroup"
)

const (
	RankerTaskQueue = "ranker-task-queue"
)

type Service struct {
	jobRepo        JobRepository
	env            EnvConfig
	genAiClient    *VertexGenAIClient
	temporalClient client.Client
	httpServer     *http.Server
}

func NewService(ctx context.Context, env EnvConfig, repo JobRepository) (*Service, error) {
	genAiClient, err := NewVertexAIGeminiClient(ctx, env.ProjectID, env.Region)
	if err != nil {
		return nil, fmt.Errorf("creating vertex genai client: %w", err)
	}
	temporalClient, err := client.Dial(client.Options{
		HostPort: fmt.Sprintf("%s:%d", env.TemporalHost, env.TemporalPort),
	})
	if err != nil {
		return nil, fmt.Errorf("creating temporal client: %w", err)
	}
	return &Service{
		jobRepo:        repo,
		genAiClient:    genAiClient,
		env:            env,
		temporalClient: temporalClient,
	}, nil
}

func (s *Service) Run(ctx context.Context) error {
	w := s.createTemporalWorker()
	httpServer := createHttpServer(s)
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server errored", "error", err)
			return err
		}
		return nil
	})
	g.Go(func() error {
		stopCh := make(chan interface{})
		go func() {
			<-ctx.Done()
			close(stopCh)
		}()
		if err := w.Run(stopCh); err != nil {
			slog.Error("worker errored", "error", err)
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	})
	return g.Wait()
}

func (s *Service) createTemporalWorker() worker.Worker {
	w := worker.New(s.temporalClient, RankerTaskQueue, worker.Options{
		// Cap concurrent activity executions across all workflows on this worker.
		// This is the global throttle for live Gemini calls; scale by raising it
		// (up to Vertex quota) or by adding workers.
		MaxConcurrentActivityExecutionSize: 50,
	})
	w.RegisterWorkflow(s.RankWorkflow)
	w.RegisterActivity(s.CreateJobActivity)
	w.RegisterActivity(s.GenerateRankingActivity)
	w.RegisterActivity(s.FinalizeJobActivity)
	return w
}

func createHttpServer(s *Service) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.Health)
	mux.HandleFunc("/ready", s.Ready)
	mux.HandleFunc("/workflow/run/", s.RunWorkflow)
	return &http.Server{
		Addr:    fmt.Sprintf("[::]:%d", s.env.HttpPort),
		Handler: mux,
	}
}

func (s *Service) Health(writer http.ResponseWriter, request *http.Request) {
	writer.WriteHeader(http.StatusOK)
}

func (s *Service) Ready(writer http.ResponseWriter, request *http.Request) {
	writer.WriteHeader(http.StatusOK)
}

func (s *Service) RunWorkflow(writer http.ResponseWriter, request *http.Request) {
	templateID := request.URL.Query().Get("template_id")
	if templateID == "" {
		http.Error(writer, "template_id query parameter is required", http.StatusBadRequest)
		return
	}

	run, err := s.temporalClient.ExecuteWorkflow(
		request.Context(),
		client.StartWorkflowOptions{
			TaskQueue: RankerTaskQueue,
		},
		s.RankWorkflow,
		templateID,
	)
	if err != nil {
		slog.Error("starting workflow", "error", err)
		http.Error(writer, "failed to start workflow", http.StatusInternalServerError)
		return
	}

	slog.Info("started workflow", "workflow_id", run.GetID(), "run_id", run.GetRunID())
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(writer).Encode(map[string]string{
		"workflow_id": run.GetID(),
		"run_id":      run.GetRunID(),
	})
}
