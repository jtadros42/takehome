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
	otherJobID       = "job-2"

	sampleID0     = "job-1-s0"
	sampleID1     = "job-1-s1"
	otherSampleID = "job-2-s0"
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
				Status:   SampleStatusPending,
			},
			{
				JobID:    testJobID,
				SampleID: sampleID0,
				Status:   SampleStatusPending,
			},
			{
				JobID:    testJobID,
				SampleID: sampleID1,
				Status:   SampleStatusPending,
			},
			{
				JobID:    otherJobID,
				SampleID: otherSampleID,
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
	require.NoError(t, repo.UpsertRankJob(ctx, EvertuneRankJob{JobID: testJobID, Status: JobStatusCompleted}))
	got, err = repo.GetRankJob(ctx, testJobID)
	require.NoError(t, err)
	assert.Equal(t, JobStatusCompleted, got.Status)
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

func TestInMemoryRepo_UpsertSamplesOverwritesInPlace(t *testing.T) {
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
				{JobID: testJobID, SampleID: sampleID0, Status: SampleStatusSucceeded, FinishReason: "STOP"},
			},
			expected: map[string]expectedSample{
				sampleID0: {SampleStatusSucceeded, "STOP"},
				sampleID1: {SampleStatusPending, ""},
			},
		},
		{
			name: "all results land",
			scatter: []EvertuneSample{
				{JobID: testJobID, SampleID: sampleID0, Status: SampleStatusSucceeded, FinishReason: "STOP"},
				{JobID: testJobID, SampleID: sampleID1, Status: SampleStatusSucceeded, FinishReason: "STOP"},
			},
			expected: map[string]expectedSample{
				sampleID0: {SampleStatusSucceeded, "STOP"},
				sampleID1: {SampleStatusSucceeded, "STOP"},
			},
		},
		{
			name: "a failure lands with its finish reason",
			scatter: []EvertuneSample{
				{JobID: testJobID, SampleID: sampleID0, Status: SampleStatusFailed, FinishReason: "SAFETY"},
			},
			expected: map[string]expectedSample{
				sampleID0: {SampleStatusFailed, "SAFETY"},
				sampleID1: {SampleStatusPending, ""},
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
		JobID:        "job-1",
		SampleID:     "job-1-p0-s0",
		Provider:     ProviderVertexAI,
		Model:        "gemini-2.5-flash-002",
		Status:       SampleStatusSucceeded,
		FinishReason: "STOP",
		InputTokens:  662,
		OutputTokens: 197,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		CompletedAt:  time.Now(),
	}

	b.ReportAllocs()
	for b.Loop() {
		_ = deepCopy(sample)
	}
}
