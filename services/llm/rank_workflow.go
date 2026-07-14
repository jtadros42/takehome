package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jtadros42/takehome/services/llm/resources"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/genai"
)

const (
	maxConcurrentSamples = 10
	maxParseAttempts     = 5
	parseErrorType       = "ParseError"
	apiErrorType         = "APIError"
	maxFailureRate       = 0.10
)

// permanentAPIStatuses are http statuses that will fail without retry
var permanentAPIStatuses = map[int]bool{
	http.StatusBadRequest:   true, // 400 — malformed request
	http.StatusUnauthorized: true, // 401 — bad credentials
	http.StatusForbidden:    true, // 403 — not permitted
	http.StatusNotFound:     true, // 404 — unknown model/resource
}

type CreateJobResult struct {
	Job     EvertuneRankJob  `json:"job"`
	Samples []EvertuneSample `json:"samples"`
}

func (s *Service) RankWorkflow(ctx workflow.Context, templateID string) (*EvertuneRankJob, error) {
	// The intention of this configuration is to allow 429s to retry
	// for an extended period of time once tokens/request quota is exhausted
	// It will retry until the ScheduleToCloseTimeout is reached (default 2 hours)
	// Non retryable errors will fail immediately. Other errors, like invalid
	// response format from the LLM, will retry up to a maxAttempts and fail. These
	// retries are handled in the activity
	ctx = workflow.WithActivityOptions(ctx,
		workflow.ActivityOptions{
			StartToCloseTimeout:    30 * time.Second,
			ScheduleToCloseTimeout: 2 * time.Hour,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval:    time.Second,
				BackoffCoefficient: 2.0,
				MaximumInterval:    time.Minute,
				MaximumAttempts:    0,
			},
		},
	)
	jobID := workflow.GetInfo(ctx).WorkflowExecution.ID
	var created CreateJobResult
	if err := workflow.ExecuteActivity(
		ctx,
		s.CreateJobActivity,
		templateID,
		jobID,
		workflow.Now(ctx),
	).Get(ctx, &created); err != nil {
		return nil, err
	}

	sem := workflow.NewBufferedChannel(ctx, maxConcurrentSamples)
	done := workflow.NewChannel(ctx)

	// Fan out one Gemini call per sample, bounded to maxConcurrentSamples.
	for _, sample := range created.Samples {
		sample := sample
		sem.Send(ctx, nil)
		workflow.Go(ctx, func(ctx workflow.Context) {
			defer func() {
				sem.Receive(ctx, nil)
				done.Send(ctx, nil)
			}()
			_ = workflow.ExecuteActivity(ctx, s.GenerateRankingActivity, sample).Get(ctx, nil)
		})
	}

	// Wait for every spawned coroutine before reconciling.
	for range created.Samples {
		done.Receive(ctx, nil)
	}

	// Tally the persisted sample outcomes into the job's final status + counts.
	var job EvertuneRankJob
	if err := workflow.ExecuteActivity(
		ctx,
		s.FinalizeJobActivity,
		jobID,
	).Get(ctx, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

func (s *Service) CreateJobActivity(ctx context.Context, templateID, jobID string, runTime time.Time) (*CreateJobResult, error) {
	tmpl, err := s.jobRepo.GetJobTemplate(ctx, templateID)
	if err != nil {
		return nil, fmt.Errorf("getting template %q: %w", templateID, err)
	}
	job, samples := ExpandTemplate(
		&tmpl,
		jobID,
		runTime,
	)
	if err := s.jobRepo.UpsertRankJob(ctx, job); err != nil {
		return nil, err
	}
	if err := s.jobRepo.UpsertSamples(ctx, samples); err != nil {
		return nil, err
	}
	return &CreateJobResult{
		Job:     job,
		Samples: samples,
	}, nil
}

func (s *Service) GenerateRankingActivity(ctx context.Context, sample EvertuneSample) (*EvertuneSample, error) {
	request, err := s.buildSampleRequest(ctx, sample)
	if err != nil {
		return nil, err
	}

	// Re-roll on unparseable responses up to maxParseAttempts. A real API error
	// (e.g. a 429) returns immediately so Temporal's RetryPolicy handles it with
	// backoff
	var response *GenerateResult
	var parseErr error
	for range maxParseAttempts {
		response, err = s.genAiClient.Generate(ctx, *request)
		if err != nil {
			return nil, classifyGenerateError(err)
		}
		if _, parseErr = parseRankResponse([]byte(response.Text)); parseErr == nil {
			break
		}
	}

	if parseErr == nil {
		sample.Status = SampleStatusSucceeded
	} else {
		sample.Status = SampleStatusFailed
	}

	sample.populateFromResponse(response)
	sample.CompletedAt = time.Now().UTC()
	sample.UpdatedAt = sample.CompletedAt

	if err := s.jobRepo.UpsertSamples(ctx, []EvertuneSample{sample}); err != nil {
		return nil, fmt.Errorf("upserting sample: %w", err)
	}

	if parseErr != nil {
		// Every re-roll failed to parse; stop (terminal — no Temporal retry).
		return nil, temporal.NewNonRetryableApplicationError(
			"unparseable rank response", parseErrorType, parseErr,
		)
	}
	return &sample, nil
}

func (s *Service) FinalizeJobActivity(ctx context.Context, jobID string) (*EvertuneRankJob, error) {
	job, err := s.jobRepo.GetRankJob(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("getting job %q: %w", jobID, err)
	}
	samples, err := s.jobRepo.ListSamplesByJob(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("listing samples for job %q: %w", jobID, err)
	}

	var succeeded, failed int
	for _, sample := range samples {
		switch sample.Status {
		case SampleStatusSucceeded:
			succeeded++
		case SampleStatusFailed:
			failed++
		}
	}

	job.SucceededCount = succeeded
	job.FailedCount = failed
	job.Ranking = AggregateRanking(samples)
	job.Status = JobStatusCompleted

	total := succeeded + failed
	if total > 0 && float64(failed)/float64(total) >= maxFailureRate {
		job.Status = JobStatusFailed
	}

	job.UpdatedAt = time.Now().UTC()
	if err := s.jobRepo.UpsertRankJob(ctx, job); err != nil {
		return nil, fmt.Errorf("upserting completed job: %w", err)
	}
	return &job, nil
}

func (s *Service) buildSampleRequest(ctx context.Context, sample EvertuneSample) (*GenerateRequest, error) {
	job, err := s.jobRepo.GetRankJob(ctx, sample.JobID)
	if err != nil {
		return nil, fmt.Errorf("getting job %q: %w", sample.JobID, err)
	}
	tmpl, err := s.jobRepo.GetJobTemplate(ctx, job.TemplateID)
	if err != nil {
		return nil, fmt.Errorf("getting template %q: %w", job.TemplateID, err)
	}
	systemPrompt, err := resources.SystemPrompt("ranker")
	if err != nil {
		return nil, fmt.Errorf("loading ranker prompt: %w", err)
	}
	userPrompt, err := json.Marshal(RankRequest{
		Category: tmpl.Category,
		Region:   tmpl.Region,
		TopN:     tmpl.TopN,
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling rank request: %w", err)
	}
	return &GenerateRequest{
		Prompt:       string(userPrompt),
		SystemPrompt: systemPrompt,
		Model:        string(sample.Model),
	}, nil
}

func classifyGenerateError(err error) error {
	var apiErr genai.APIError
	if errors.As(err, &apiErr) && permanentAPIStatuses[apiErr.Code] {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("gemini generate: permanent error %d", apiErr.Code), apiErrorType, err,
		)
	}
	return fmt.Errorf("gemini generate: %w", err)
}
