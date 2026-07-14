package llm

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
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

	// Mock the per-sample Gemini call so this stays a pure orchestration test
	// (no network). Assert the workflow fans out one call per sample.
	var sampleCalls int
	env.OnActivity(s.GenerateSampleActivity, mock.Anything, mock.Anything).
		Return(func(_ context.Context, sample EvertuneSample) (EvertuneSample, error) {
			sampleCalls++
			sample.Status = SampleStatusSucceeded
			return sample, nil
		})

	env.ExecuteWorkflow(s.RankWorkflow, wfTemplateID)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result EvertuneRankJob
	require.NoError(t, env.GetWorkflowResult(&result))
	assert.Equal(t, defaultTestWorkflowID, result.JobID)
	assert.Equal(t, wfTemplateID, result.TemplateID)
	assert.Equal(t, JobStatusCreated, result.Status)

	// SamplesPerCell=2 with one provider means two fan-out calls.
	assert.Equal(t, 2, sampleCalls)

	stored, err := s.jobRepo.GetRankJob(context.Background(), defaultTestWorkflowID)
	require.NoError(t, err)
	assert.Equal(t, result, stored)
}
