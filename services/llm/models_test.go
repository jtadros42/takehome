package llm

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJobStatus_Values(t *testing.T) {
	assert.Equal(t, "CREATED", string(JobStatusCreated))
	assert.Equal(t, "REPORT_READY", string(JobStatusReportReady))
	assert.Equal(t, "FAILED", string(JobStatusFailed))
	assert.Equal(t, "SUCCEEDED", string(SampleStatusSucceeded))
}

const (
	testTemplateID   = "template-1"
	testTemplateName = "luxury tracker"
	testJobID        = "job-1"
	testModel        = "gemini-2.5-flash-002"
	testCategory     = "luxury car brands"
	testRegion       = "US"
	testTopN         = 10
	testSamples      = 100
)

func TestEvertuneTemplate_Serialization(t *testing.T) {
	tmpl := EvertuneJobTemplate{
		TemplateID:     testTemplateID,
		Name:           testTemplateName,
		Category:       testCategory,
		Region:         testRegion,
		TopN:           testTopN,
		SamplesPerCell: testSamples,
		ModelProviders: []ModelProvider{
			{
				Provider: ProviderVertexAI,
				Model:    testModel,
			},
		},
	}

	data, err := json.Marshal(tmpl)
	require.NoError(t, err)
	var got EvertuneJobTemplate
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, tmpl, got)
}

func TestEvertuneJob_Serialization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	job := EvertuneRankJob{
		JobID:       testJobID,
		TemplateID:  testTemplateID,
		Status:      JobStatusCreated,
		SampleCount: 200,
		RequestedAt: now,
		UpdatedAt:   now,
	}

	data, err := json.Marshal(job)
	require.NoError(t, err)
	var got EvertuneRankJob
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, job, got)
}

func TestExpandTemplate(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	testCases := []struct {
		name            string
		template        EvertuneJobTemplate
		jobID           string
		expectedJob     EvertuneRankJob
		expectedSamples []EvertuneSample
	}{
		{
			name: "two providers, two samples per cell",
			template: EvertuneJobTemplate{
				TemplateID:     testTemplateID,
				Category:       testCategory,
				Region:         testRegion,
				TopN:           testTopN,
				SamplesPerCell: 2,
				ModelProviders: []ModelProvider{
					{
						Provider: ProviderVertexAI,
						Model:    testModel,
					},
					{
						Provider: ProviderTogetherAI,
						Model:    "llama-3.3-70b",
					},
				},
			},
			jobID: testJobID,
			expectedJob: EvertuneRankJob{
				JobID:       testJobID,
				TemplateID:  testTemplateID,
				Status:      JobStatusCreated,
				SampleCount: 4,
				RequestedAt: now,
				UpdatedAt:   now,
			},
			expectedSamples: []EvertuneSample{
				{
					JobID:     testJobID,
					SampleID:  "job-1-p0-s0",
					Provider:  ProviderVertexAI,
					Model:     testModel,
					Status:    SampleStatusPending,
					CreatedAt: now,
					UpdatedAt: now,
				},
				{
					JobID:     testJobID,
					SampleID:  "job-1-p0-s1",
					Provider:  ProviderVertexAI,
					Model:     testModel,
					Status:    SampleStatusPending,
					CreatedAt: now,
					UpdatedAt: now,
				},
				{
					JobID:     testJobID,
					SampleID:  "job-1-p1-s0",
					Provider:  ProviderTogetherAI,
					Model:     "llama-3.3-70b",
					Status:    SampleStatusPending,
					CreatedAt: now,
					UpdatedAt: now,
				},
				{
					JobID:     testJobID,
					SampleID:  "job-1-p1-s1",
					Provider:  ProviderTogetherAI,
					Model:     "llama-3.3-70b",
					Status:    SampleStatusPending,
					CreatedAt: now,
					UpdatedAt: now,
				},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			job, samples := ExpandTemplate(&testCase.template, testCase.jobID, now)
			assert.Equal(t, testCase.expectedJob, job)
			assert.Equal(t, testCase.expectedSamples, samples)
		})
	}
}

func TestExpandTemplate_IsDeterministic(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	tmpl := &EvertuneJobTemplate{
		TemplateID:     testTemplateID,
		Category:       testCategory,
		TopN:           testTopN,
		SamplesPerCell: 2,
		ModelProviders: []ModelProvider{
			{
				Provider: ProviderVertexAI,
				Model:    testModel,
			},
		},
	}
	job1, samples1 := ExpandTemplate(tmpl, testJobID, now)
	job2, samples2 := ExpandTemplate(tmpl, testJobID, now)
	assert.Equal(t, job1, job2)
	assert.Equal(t, samples1, samples2)
}

func TestExpandTemplate_EmptyProviders(t *testing.T) {
	now := time.Now().UTC()
	tmpl := &EvertuneJobTemplate{TemplateID: testTemplateID, Category: testCategory, SamplesPerCell: 5}
	job, samples := ExpandTemplate(tmpl, testJobID, now)
	assert.Zero(t, job.SampleCount)
	assert.Empty(t, samples)
}
