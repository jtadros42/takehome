package llm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/genai"
)

const (
	wfTemplateID          = "luxury-cars-us"
	wfTemplateName        = "Luxury Car Brands (US)"
	wfJobID               = "job-under-test"
	defaultTestWorkflowID = "default-test-workflow-id"
)

func newWorkflowTestService() *Service {
	repo := NewInMemoryJobRepository().WithJobTemplates([]EvertuneJobTemplate{
		{
			TemplateID:     wfTemplateID,
			Name:           wfTemplateName,
			Category:       testCategory,
			Region:         testRegion,
			TopN:           testTopN,
			SamplesPerCell: 2,
			ModelProviders: []ModelProvider{
				{Provider: ProviderVertexAI, Model: testModel},
			},
		},
	})
	return &Service{jobRepo: repo}
}

func TestCreateJobActivity(t *testing.T) {
	tests := []struct {
		name       string
		templateID string
		wantErr    error
	}{
		{
			name:       "known template expands and persists job",
			templateID: wfTemplateID,
		},
		{
			name:       "unknown template returns not found",
			templateID: "does-not-exist",
			wantErr:    ErrNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newWorkflowTestService()
			ctx := context.Background()

			result, err := s.CreateJobActivity(ctx, tt.templateID, wfJobID, time.Now().UTC())

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)

			assert.Equal(t, wfJobID, result.Job.JobID)
			assert.Equal(t, tt.templateID, result.Job.TemplateID)
			assert.Equal(t, JobStatusCreated, result.Job.Status)
			assert.Equal(t, 2, result.Job.SampleCount)

			// The returned samples match what was persisted.
			assert.Len(t, result.Samples, 2)

			// The rows were actually persisted, keyed on jobID.
			stored, err := s.jobRepo.GetRankJob(ctx, wfJobID)
			require.NoError(t, err)
			assert.Equal(t, result.Job, stored)

			samples, err := s.jobRepo.ListSamplesByJob(ctx, wfJobID)
			require.NoError(t, err)
			assert.Len(t, samples, 2)
		})
	}
}

func TestRankWorkflow(t *testing.T) {
	s := newWorkflowTestService()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivity(s.CreateJobActivity)
	env.RegisterActivity(s.FinalizeJobActivity)

	// Mock the per-sample Gemini call so this stays a pure orchestration test
	// (no network). It persists each sample as SUCCEEDED, mirroring the real
	// activity, so FinalizeJobActivity can reconcile from the store.
	var sampleCalls int
	env.OnActivity(s.GenerateRankingActivity, mock.Anything, mock.Anything).
		Return(func(ctx context.Context, sample EvertuneSample) (*EvertuneSample, error) {
			sampleCalls++
			sample.Status = SampleStatusSucceeded
			if err := s.jobRepo.UpsertSamples(ctx, []EvertuneSample{sample}); err != nil {
				return nil, err
			}
			return &sample, nil
		})

	env.ExecuteWorkflow(s.RankWorkflow, wfTemplateID)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result EvertuneRankJob
	require.NoError(t, env.GetWorkflowResult(&result))
	assert.Equal(t, defaultTestWorkflowID, result.JobID)
	assert.Equal(t, wfTemplateID, result.TemplateID)

	// SamplesPerCell=2 with one provider means two fan-out calls, all succeeding,
	// so the job completes with both counted.
	assert.Equal(t, 2, sampleCalls)
	assert.Equal(t, JobStatusCompleted, result.Status)
	assert.Equal(t, 2, result.SucceededCount)
	assert.Equal(t, 0, result.FailedCount)

	stored, err := s.jobRepo.GetRankJob(context.Background(), defaultTestWorkflowID)
	require.NoError(t, err)
	assert.Equal(t, result, stored)
}

// seedSamples returns a slice of statusOnly samples for a job: succeeded of
// them SUCCEEDED, failed of them FAILED, pending of them PENDING.
func seedSamples(jobID string, succeeded, failed, pending int) []EvertuneSample {
	var samples []EvertuneSample
	add := func(n int, status SampleStatus) {
		for range n {
			id := SampleIDFor(jobID, 0, len(samples))
			samples = append(samples, EvertuneSample{JobID: jobID, SampleID: id, Status: status})
		}
	}
	add(succeeded, SampleStatusSucceeded)
	add(failed, SampleStatusFailed)
	add(pending, SampleStatusPending)
	return samples
}

func TestFinalizeJobActivity(t *testing.T) {
	tests := []struct {
		name       string
		succeeded  int
		failed     int
		pending    int
		wantStatus JobStatus
	}{
		{
			name:       "all succeed completes",
			succeeded:  10,
			wantStatus: JobStatusCompleted,
		},
		{
			name:       "just under threshold completes",
			succeeded:  92,
			failed:     8, // 8% < 10%
			wantStatus: JobStatusCompleted,
		},
		{
			name:       "exactly at threshold fails",
			succeeded:  9,
			failed:     1, // 10% >= 10%
			wantStatus: JobStatusFailed,
		},
		{
			name:       "all fail fails",
			failed:     5,
			wantStatus: JobStatusFailed,
		},
		{
			name:       "no terminal samples completes with zero counts",
			pending:    4,
			wantStatus: JobStatusCompleted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			repo := NewInMemoryJobRepository().
				WithJobs([]EvertuneRankJob{{JobID: wfJobID, TemplateID: wfTemplateID, Status: JobStatusCreated}}).
				WithSamples(seedSamples(wfJobID, tt.succeeded, tt.failed, tt.pending))
			s := &Service{jobRepo: repo}

			job, err := s.FinalizeJobActivity(ctx, wfJobID)
			require.NoError(t, err)

			assert.Equal(t, tt.wantStatus, job.Status)
			assert.Equal(t, tt.succeeded, job.SucceededCount)
			assert.Equal(t, tt.failed, job.FailedCount)

			// The reconciled status is persisted, not just returned.
			stored, err := repo.GetRankJob(ctx, wfJobID)
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, stored.Status)
		})
	}
}

const validRankingJSON = `{"rankings":[{"rank":1,"brand":"Mercedes"}]}`

func TestGenerateRankingActivity_SucceedsWithoutReRolling(t *testing.T) {
	tests := []struct {
		name   string
		sample EvertuneSample
	}{
		{
			name:   "parseable first response is not re-rolled",
			sample: EvertuneSample{JobID: wfJobID, SampleID: "s0", Provider: ProviderVertexAI, Model: testModel},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			client := &VertexAIClientMock{
				GenerateMock: func(context.Context, GenerateRequest) (*GenerateResult, error) {
					calls++
					return &GenerateResult{Text: validRankingJSON, FinishReason: "STOP"}, nil
				},
			}
			s := generateRankingTestService(client)

			got, err := s.GenerateRankingActivity(context.Background(), tt.sample)
			require.NoError(t, err)
			assert.Equal(t, 1, calls, "a parseable first response must not re-roll")
			assert.Equal(t, SampleStatusSucceeded, got.Status)

			stored, err := s.jobRepo.ListSamplesByJob(context.Background(), wfJobID)
			require.NoError(t, err)
			require.Len(t, stored, 1)
			assert.Equal(t, SampleStatusSucceeded, stored[0].Status)
		})
	}
}

func TestGenerateRankingActivity_ParseFailuresAreRetried(t *testing.T) {
	tests := []struct {
		name          string
		sample        EvertuneSample
		succeedOnCall int
	}{
		{
			name:          "re-rolls past unparseable responses until one parses",
			sample:        EvertuneSample{JobID: wfJobID, SampleID: "s0", Provider: ProviderVertexAI, Model: testModel},
			succeedOnCall: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			client := &VertexAIClientMock{
				GenerateMock: func(context.Context, GenerateRequest) (*GenerateResult, error) {
					calls++
					if calls < tt.succeedOnCall {
						return &GenerateResult{Text: "not json"}, nil
					}
					return &GenerateResult{Text: validRankingJSON}, nil
				},
			}
			s := generateRankingTestService(client)

			got, err := s.GenerateRankingActivity(context.Background(), tt.sample)
			require.NoError(t, err)
			assert.Equal(t, tt.succeedOnCall, calls, "should re-roll until a response parses")
			assert.Equal(t, SampleStatusSucceeded, got.Status)
		})
	}
}

func TestGenerateRankingActivity_FailuresExhaustAttempts(t *testing.T) {
	tests := []struct {
		name    string
		sample  EvertuneSample
		rawText string
	}{
		{
			name:    "every re-roll fails to parse",
			sample:  EvertuneSample{JobID: wfJobID, SampleID: "s0", Provider: ProviderVertexAI, Model: testModel},
			rawText: "still not json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			client := &VertexAIClientMock{
				GenerateMock: func(context.Context, GenerateRequest) (*GenerateResult, error) {
					calls++
					return &GenerateResult{Text: tt.rawText}, nil
				},
			}
			s := generateRankingTestService(client)

			got, err := s.GenerateRankingActivity(context.Background(), tt.sample)
			require.Error(t, err)
			assert.Nil(t, got)
			assert.Equal(t, maxParseAttempts, calls, "should exhaust exactly maxParseAttempts re-rolls")

			// The error is terminal (non-retryable) so Temporal will not retry it.
			var appErr *temporal.ApplicationError
			require.ErrorAs(t, err, &appErr)
			assert.True(t, appErr.NonRetryable(), "parse exhaustion must be non-retryable")
			assert.Equal(t, parseErrorType, appErr.Type())

			// The failed sample is persisted with its raw response for inspection.
			stored, err := s.jobRepo.ListSamplesByJob(context.Background(), wfJobID)
			require.NoError(t, err)
			require.Len(t, stored, 1)
			assert.Equal(t, SampleStatusFailed, stored[0].Status)
			assert.Equal(t, tt.rawText, stored[0].RawResponse)
		})
	}
}

func TestGenerateRankingActivity_ClassifiesAPIErrors(t *testing.T) {
	sample := EvertuneSample{JobID: wfJobID, SampleID: "s0", Provider: ProviderVertexAI, Model: testModel}
	tests := []struct {
		name         string
		apiErr       error
		wantTerminal bool // true = non-retryable (permanent), false = retryable (transient)
	}{
		{
			name:         "rate limit is transient and stays retryable",
			apiErr:       genai.APIError{Code: 429, Message: "rate limited"},
			wantTerminal: false,
		},
		{
			name:         "server error is transient and stays retryable",
			apiErr:       genai.APIError{Code: 503, Message: "unavailable"},
			wantTerminal: false,
		},
		{
			name:         "non-API error (e.g. network) stays retryable",
			apiErr:       errors.New("connection reset"),
			wantTerminal: false,
		},
		{
			name:         "bad request is permanent and fails terminally",
			apiErr:       genai.APIError{Code: 400, Message: "invalid argument"},
			wantTerminal: true,
		},
		{
			name:         "auth failure is permanent and fails terminally",
			apiErr:       genai.APIError{Code: 403, Message: "permission denied"},
			wantTerminal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			client := &VertexAIClientMock{
				GenerateMock: func(context.Context, GenerateRequest) (*GenerateResult, error) {
					calls++
					return nil, tt.apiErr
				},
			}
			s := generateRankingTestService(client)

			got, err := s.GenerateRankingActivity(context.Background(), sample)
			require.Error(t, err)
			assert.Nil(t, got)
			assert.Equal(t, 1, calls, "an API error must not be treated as a parse re-roll")

			var appErr *temporal.ApplicationError
			if tt.wantTerminal {
				require.ErrorAs(t, err, &appErr)
				assert.True(t, appErr.NonRetryable(), "permanent API errors must be non-retryable")
			} else if errors.As(err, &appErr) {
				assert.False(t, appErr.NonRetryable(), "transient API errors must stay retryable")
			}

			// Nothing was persisted; the sample never reached a terminal state.
			stored, err := s.jobRepo.ListSamplesByJob(context.Background(), wfJobID)
			require.NoError(t, err)
			assert.Empty(t, stored)
		})
	}
}

func TestAggregateRanking(t *testing.T) {
	const otherModel SupportedModel = "llama-3.3-70b"

	tests := []struct {
		name    string
		samples []EvertuneSample
		want    []ModelRanking
	}{
		{
			name:    "no samples yields no rankings",
			samples: nil,
			want:    []ModelRanking{},
		},
		{
			name: "mean rank and count across samples, sorted strongest first",
			samples: []EvertuneSample{
				rankedSample(t, ProviderVertexAI, testModel, "Mercedes", "BMW", "Audi"),
				rankedSample(t, ProviderVertexAI, testModel, "BMW", "Mercedes", "Audi"),
			},
			want: []ModelRanking{
				{
					Provider: ProviderVertexAI,
					Model:    testModel,
					Brands: []BrandStat{
						// Mercedes: 1,2 -> 1.5 ; BMW: 2,1 -> 1.5 (tie broken by brand) ; Audi: 3,3 -> 3
						{Brand: "BMW", MeanRank: 1.5, SampleCount: 2},
						{Brand: "Mercedes", MeanRank: 1.5, SampleCount: 2},
						{Brand: "Audi", MeanRank: 3, SampleCount: 2},
					},
				},
			},
		},
		{
			name: "each model is aggregated separately",
			samples: []EvertuneSample{
				rankedSample(t, ProviderVertexAI, testModel, "Mercedes", "BMW"),
				rankedSample(t, ProviderTogetherAI, otherModel, "BMW", "Mercedes"),
			},
			want: []ModelRanking{
				{
					Provider: ProviderTogetherAI,
					Model:    otherModel,
					Brands: []BrandStat{
						{Brand: "BMW", MeanRank: 1, SampleCount: 1},
						{Brand: "Mercedes", MeanRank: 2, SampleCount: 1},
					},
				},
				{
					Provider: ProviderVertexAI,
					Model:    testModel,
					Brands: []BrandStat{
						{Brand: "Mercedes", MeanRank: 1, SampleCount: 1},
						{Brand: "BMW", MeanRank: 2, SampleCount: 1},
					},
				},
			},
		},
		{
			name: "non-succeeded and unparseable samples are skipped",
			samples: []EvertuneSample{
				rankedSample(t, ProviderVertexAI, testModel, "Mercedes", "BMW"),
				{Provider: ProviderVertexAI, Model: testModel, Status: SampleStatusFailed, RawResponse: "garbage"},
				{Provider: ProviderVertexAI, Model: testModel, Status: SampleStatusSucceeded, RawResponse: "not json"},
			},
			want: []ModelRanking{
				{
					Provider: ProviderVertexAI,
					Model:    testModel,
					Brands: []BrandStat{
						{Brand: "Mercedes", MeanRank: 1, SampleCount: 1},
						{Brand: "BMW", MeanRank: 2, SampleCount: 1},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AggregateRanking(tt.samples)
			require.Len(t, got, len(tt.want))
			assert.Equal(t, tt.want, got)
		})
	}
}

func generateRankingTestService(client VertexAIClient) *Service {
	repo := NewInMemoryJobRepository().
		WithJobTemplates([]EvertuneJobTemplate{
			{
				TemplateID: wfTemplateID,
				Category:   testCategory,
				Region:     testRegion,
				TopN:       testTopN,
				ModelProviders: []ModelProvider{
					{
						Provider: ProviderVertexAI,
						Model:    testModel,
					},
				},
			}}).
		WithJobs([]EvertuneRankJob{
			{
				JobID:      wfJobID,
				TemplateID: wfTemplateID,
				Status:     JobStatusCreated,
			},
		})
	return &Service{jobRepo: repo, genAiClient: client}
}

func rankedSample(t *testing.T, provider Provider, model SupportedModel, brands ...string) EvertuneSample {
	t.Helper()
	ranks := make([]Rank, len(brands))
	for i, brand := range brands {
		ranks[i] = Rank{Rank: i + 1, Brand: brand}
	}
	raw, err := json.Marshal(RankResponse{Rankings: ranks})
	require.NoError(t, err)
	return EvertuneSample{
		Provider:    provider,
		Model:       model,
		Status:      SampleStatusSucceeded,
		RawResponse: string(raw),
	}
}

type VertexAIClientMock struct {
	GenerateMock    func(ctx context.Context, request GenerateRequest) (*GenerateResult, error)
	CountTokensMock func(ctx context.Context, model string, text string) (int32, error)
}

func (c *VertexAIClientMock) Generate(ctx context.Context, request GenerateRequest) (*GenerateResult, error) {
	if c.GenerateMock != nil {
		return c.GenerateMock(ctx, request)
	}
	return nil, errors.New("not Implemented")
}

func (c *VertexAIClientMock) CountTokens(ctx context.Context, model string, text string) (int32, error) {
	if c.CountTokensMock != nil {
		return c.CountTokensMock(ctx, model, text)
	}
	return 0, errors.New("not Implemented")
}
