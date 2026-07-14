package llm

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	seedTemplateID   = "seed-tmpl"
	seedTemplateName = "seed luxury tracker"
	seedJobID        = "seed-job"
	seedSampleID     = "seed-job-s0"
	seedBatchID      = "seed-batch"
	otherJobID       = "job-2"

	sampleID0     = "job-1-s0"
	sampleID1     = "job-1-s1"
	otherSampleID = "job-2-s0"
	sharedBatchID = "batch-47"
	soloBatchID   = "batch-48"
)

func getTestInMemoryRepo() *InMemoryJobRepository {
	return NewInMemoryJobRepository().
		WithJobTemplates([]EvertuneJobTemplate{{
			TemplateID:     seedTemplateID,
			Name:           seedTemplateName,
			Category:       testCategory,
			Region:         testRegion,
			TopN:           testTopN,
			SamplesPerCell: 1,
			ModelProviders: []ModelProvider{
				{
					Provider: ProviderVertexAI,
					Model:    testModel,
				},
			},
		}}).
		WithJobs([]EvertuneRankJob{
			{
				JobID:       seedJobID,
				TemplateID:  seedTemplateID,
				Status:      JobStatusCreated,
				SampleCount: 1,
			},
		}).
		WithSamples([]EvertuneSample{
			{
				JobID:    seedJobID,
				SampleID: seedSampleID,
				Provider: ProviderVertexAI,
				BatchID:  seedBatchID,
				Status:   SampleStatusPending,
			},
			{
				JobID:    testJobID,
				SampleID: sampleID0,
				BatchID:  sharedBatchID,
				Status:   SampleStatusSubmitted,
			},
			{
				JobID:    testJobID,
				SampleID: sampleID1,
				BatchID:  soloBatchID,
				Status:   SampleStatusSubmitted,
			},
			{
				JobID:    otherJobID,
				SampleID: otherSampleID,
				BatchID:  sharedBatchID,
				Status:   SampleStatusPending,
			},
		})
}

func TestInMemoryRepo_JobTemplateNotFound(t *testing.T) {
	repo := getTestInMemoryRepo()
	ctx := t.Context()
	_, err := repo.GetJobTemplate(ctx, "missing")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestInMemoryRepo_RankJob(t *testing.T) {
	repo := getTestInMemoryRepo()
	ctx := t.Context()

	_, err := repo.GetRankJob(ctx, "missing")
	assert.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, repo.UpsertRankJob(ctx, EvertuneRankJob{JobID: testJobID, Status: JobStatusCreated}))
	got, err := repo.GetRankJob(ctx, testJobID)
	require.NoError(t, err)
	assert.Equal(t, JobStatusCreated, got.Status)

	// Upsert with the same JobID overwrites
	require.NoError(t, repo.UpsertRankJob(ctx, EvertuneRankJob{JobID: testJobID, Status: JobStatusPolling}))
	got, err = repo.GetRankJob(ctx, testJobID)
	require.NoError(t, err)
	assert.Equal(t, JobStatusPolling, got.Status)
}

func TestInMemoryRepo_ReturnedValueIsACopy(t *testing.T) {
	repo := getTestInMemoryRepo()
	ctx := t.Context()
	require.NoError(t, repo.UpsertRankJob(ctx, EvertuneRankJob{JobID: testJobID, Status: JobStatusCreated}))

	got, err := repo.GetRankJob(ctx, testJobID)
	require.NoError(t, err)
	got.Status = JobStatusFailed

	reread, err := repo.GetRankJob(ctx, testJobID)
	require.NoError(t, err)
	assert.Equal(t, JobStatusCreated, reread.Status, "stored job must be unaffected by caller mutation")
}

func TestInMemoryRepo_ListSamplesByJob(t *testing.T) {
	repo := getTestInMemoryRepo()
	ctx := t.Context()

	job1, err := repo.ListSamplesByJob(ctx, testJobID)
	require.NoError(t, err)
	assert.Len(t, job1, 2)

	other, err := repo.ListSamplesByJob(ctx, otherJobID)
	require.NoError(t, err)
	assert.Len(t, other, 1)

	// Empty result for unknown keys.
	none, err := repo.ListSamplesByJob(ctx, "job-missing")
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestInMemoryRepo_ListSamplesByBatch(t *testing.T) {
	testCases := []struct {
		name           string
		batchID        string
		expectedJobIDs []string
	}{
		{
			// The shared batch spans two jobs — the many-to-many the batcher
			// relies on: this is how it learns whom to signal on completion.
			name:           "shared batch spans two jobs",
			batchID:        sharedBatchID,
			expectedJobIDs: []string{testJobID, otherJobID},
		},
		{
			name:           "solo batch has one job",
			batchID:        soloBatchID,
			expectedJobIDs: []string{testJobID},
		},
		{
			name:           "unknown batch is empty",
			batchID:        "batch-missing",
			expectedJobIDs: []string{},
		},
	}

	repo := getTestInMemoryRepo()
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			samples, err := repo.ListSamplesByBatch(t.Context(), testCase.batchID)
			require.NoError(t, err)

			jobIDs := make([]string, 0, len(samples))
			for _, s := range samples {
				jobIDs = append(jobIDs, s.JobID)
			}
			assert.ElementsMatch(t, testCase.expectedJobIDs, jobIDs)
		})
	}
}

func TestInMemoryRepo_SamplesScatterResultBack(t *testing.T) {
	type expectedSample struct {
		status       SampleStatus
		finishReason string
	}
	testCases := []struct {
		name     string
		scatter  []EvertuneSample
		expected map[string]expectedSample
	}{
		{
			name: "one result lands, sibling untouched",
			scatter: []EvertuneSample{
				{JobID: testJobID, SampleID: sampleID0, BatchID: sharedBatchID, Status: SampleStatusSucceeded, FinishReason: "STOP"},
			},
			expected: map[string]expectedSample{
				sampleID0: {SampleStatusSucceeded, "STOP"},
				sampleID1: {SampleStatusSubmitted, ""},
			},
		},
		{
			name: "all results land",
			scatter: []EvertuneSample{
				{JobID: testJobID, SampleID: sampleID0, BatchID: sharedBatchID, Status: SampleStatusSucceeded, FinishReason: "STOP"},
				{JobID: testJobID, SampleID: sampleID1, BatchID: soloBatchID, Status: SampleStatusSucceeded, FinishReason: "STOP"},
			},
			expected: map[string]expectedSample{
				sampleID0: {SampleStatusSucceeded, "STOP"},
				sampleID1: {SampleStatusSucceeded, "STOP"},
			},
		},
		{
			name: "a failure lands with its finish reason",
			scatter: []EvertuneSample{
				{JobID: testJobID, SampleID: sampleID0, BatchID: sharedBatchID, Status: SampleStatusFailed, FinishReason: "SAFETY"},
			},
			expected: map[string]expectedSample{
				sampleID0: {SampleStatusFailed, "SAFETY"},
				sampleID1: {SampleStatusSubmitted, ""},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			repo := getTestInMemoryRepo()
			ctx := t.Context()
			require.NoError(t, repo.UpsertSamples(ctx, testCase.scatter))

			samples, err := repo.ListSamplesByJob(ctx, testJobID)
			require.NoError(t, err)
			require.Len(t, samples, len(testCase.expected), "overwrite must not create a duplicate")
			for _, s := range samples {
				expected, ok := testCase.expected[s.SampleID]
				require.True(t, ok, "unexpected sample %s", s.SampleID)
				assert.Equal(t, expected.status, s.Status, "status of %s", s.SampleID)
				assert.Equal(t, expected.finishReason, s.FinishReason, "finish reason of %s", s.SampleID)
			}
		})
	}
}

func TestInMemoryRepo_EmptyUpsertsAreNoOps(t *testing.T) {
	repo := getTestInMemoryRepo()
	ctx := t.Context()
	require.NoError(t, repo.UpsertSamples(ctx, []EvertuneSample{}))

	samples, err := repo.ListSamplesByJob(ctx, testJobID)
	require.NoError(t, err)
	assert.Len(t, samples, 2)
}

func BenchmarkDeepCopySample(b *testing.B) {
	sample := EvertuneSample{
		JobID:         "job-1",
		SampleID:      "job-1-p0-s0",
		Provider:      ProviderVertexAI,
		Model:         "gemini-2.5-flash-002",
		BatchID:       "batch-47",
		ExternalJobID: "vertex-123",
		Status:        SampleStatusSucceeded,
		FinishReason:  "STOP",
		ResultURI:     "gs://bucket/job-1/s0.json",
		InputTokens:   662,
		OutputTokens:  197,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		CompletedAt:   time.Now(),
	}

	b.ReportAllocs()
	for b.Loop() {
		_ = deepCopy(sample)
	}
}
