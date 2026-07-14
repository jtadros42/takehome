package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jtadros42/takehome/services/llm/resources"
	"go.temporal.io/sdk/workflow"
)

// CreateJobResult is what CreateJobActivity hands back to the workflow: the
// persisted job plus the samples to fan out over.
type CreateJobResult struct {
	Job     EvertuneRankJob  `json:"job"`
	Samples []EvertuneSample `json:"samples"`
}

func (s *Service) RankWorkflow(ctx workflow.Context, templateID string) (*EvertuneRankJob, error) {
	ctx = workflow.WithActivityOptions(ctx,
		workflow.ActivityOptions{
			StartToCloseTimeout: 30 * time.Second,
		},
	)
	jobID := workflow.GetInfo(ctx).WorkflowExecution.ID

	// Create + persist the job, tasks, and samples; get back the samples to run.
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

	// Fan out: dispatch one Gemini call per sample, all in flight at once.
	futures := make([]workflow.Future, 0, len(created.Samples))
	for _, sample := range created.Samples {
		futures = append(futures, workflow.ExecuteActivity(ctx, s.GenerateSampleActivity, sample))
	}

	// Drain every future before returning so no sample is left racing the exit.
	var firstErr error
	for _, f := range futures {
		if err := f.Get(ctx, nil); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}

	return &created.Job, nil
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
		return nil, fmt.Errorf("upserting job: %w", err)
	}
	if err := s.jobRepo.UpsertSamples(ctx, samples); err != nil {
		return nil, fmt.Errorf("upserting samples: %w", err)
	}
	return &CreateJobResult{Job: job, Samples: samples}, nil
}

// GenerateSampleActivity performs one synchronous Gemini call for a single
// sample: it builds the ranking prompt from the job's template, calls the
// model, parses the response, and persists the outcome onto the sample.
func (s *Service) GenerateSampleActivity(ctx context.Context, sample EvertuneSample) (EvertuneSample, error) {
	job, err := s.jobRepo.GetRankJob(ctx, sample.JobID)
	if err != nil {
		return EvertuneSample{}, fmt.Errorf("getting job %q: %w", sample.JobID, err)
	}
	tmpl, err := s.jobRepo.GetJobTemplate(ctx, job.TemplateID)
	if err != nil {
		return EvertuneSample{}, fmt.Errorf("getting template %q: %w", job.TemplateID, err)
	}

	systemPrompt, err := resources.SystemPrompt("ranker")
	if err != nil {
		return EvertuneSample{}, fmt.Errorf("loading ranker prompt: %w", err)
	}
	userPrompt, err := json.Marshal(RankRequest{
		Category: tmpl.Category,
		Region:   tmpl.Region,
		TopN:     tmpl.TopN,
	})
	if err != nil {
		return EvertuneSample{}, fmt.Errorf("marshaling rank request: %w", err)
	}

	result, err := s.genAiClient.Generate(ctx, GenerateRequest{
		Prompt:       string(userPrompt),
		SystemPrompt: systemPrompt,
		Model:        string(sample.Model),
	})
	if err != nil {
		return EvertuneSample{}, fmt.Errorf("gemini generate: %w", err)
	}

	sample.RawResponse = result.Text
	sample.FinishReason = result.FinishReason
	sample.InputTokens = result.InputTokenCount
	sample.OutputTokens = result.OutputTokenCount
	sample.CompletedAt = time.Now().UTC()
	sample.UpdatedAt = sample.CompletedAt

	// A response that doesn't parse into the ranking shape is a failed sample,
	// not a failed activity: we keep the raw text for inspection and record it.
	if _, err := parseRankResponse([]byte(result.Text)); err != nil {
		sample.Status = SampleStatusFailed
		if uerr := s.jobRepo.UpsertSamples(ctx, []EvertuneSample{sample}); uerr != nil {
			return EvertuneSample{}, fmt.Errorf("upserting failed sample: %w", uerr)
		}
		return sample, nil
	}

	sample.Status = SampleStatusSucceeded
	if err := s.jobRepo.UpsertSamples(ctx, []EvertuneSample{sample}); err != nil {
		return EvertuneSample{}, fmt.Errorf("upserting sample: %w", err)
	}
	return sample, nil
}
