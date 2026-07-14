package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	cmap "github.com/orcaman/concurrent-map/v2"
)

// ErrNotFound is returned when a requested entity does not exist.
var ErrNotFound = errors.New("not found")

type JobRepository interface {
	GetJobTemplate(ctx context.Context, templateID string) (EvertuneJobTemplate, error)
	GetRankJob(ctx context.Context, jobID string) (EvertuneRankJob, error)

	UpsertJobTemplate(ctx context.Context, tmpl EvertuneJobTemplate) error
	UpsertRankJob(ctx context.Context, job EvertuneRankJob) error
	UpsertSamples(ctx context.Context, samples []EvertuneSample) error

	ListSamplesByJob(ctx context.Context, jobID string) ([]EvertuneSample, error)
	ListSamplesByBatch(ctx context.Context, batchID string) ([]EvertuneSample, error)
}

// InMemoryJobRepository is a concurrency-safe, in-process JobRepository for fast
// iteration and unit tests. Every entity is keyed on its own ID
// The Cassandra implementation would have read optimized tables for each query pattern
// e.g. for ListSamplesByJob there would be a samples_by_job table that is partitioned by job_id
type InMemoryJobRepository struct {
	jobTemplates cmap.ConcurrentMap[string, EvertuneJobTemplate] // key: TemplateID
	jobs         cmap.ConcurrentMap[string, EvertuneRankJob]     // key: JobID
	samples      cmap.ConcurrentMap[string, EvertuneSample]      // key: SampleID
}

func NewInMemoryJobRepository() *InMemoryJobRepository {
	return &InMemoryJobRepository{
		jobTemplates: cmap.New[EvertuneJobTemplate](),
		jobs:         cmap.New[EvertuneRankJob](),
		samples:      cmap.New[EvertuneSample](),
	}
}

func (r *InMemoryJobRepository) WithJobTemplates(jobTemplates []EvertuneJobTemplate) *InMemoryJobRepository {
	for _, tmpl := range jobTemplates {
		_ = r.UpsertJobTemplate(context.Background(), tmpl)
	}
	return r
}

func (r *InMemoryJobRepository) WithJobs(jobs []EvertuneRankJob) *InMemoryJobRepository {
	for _, job := range jobs {
		_ = r.UpsertRankJob(context.Background(), job)
	}
	return r
}

func (r *InMemoryJobRepository) WithSamples(samples []EvertuneSample) *InMemoryJobRepository {
	_ = r.UpsertSamples(context.Background(), samples)
	return r
}

func (r *InMemoryJobRepository) GetJobTemplate(_ context.Context, templateID string) (EvertuneJobTemplate, error) {
	tmpl, ok := r.jobTemplates.Get(templateID)
	if !ok {
		return EvertuneJobTemplate{}, ErrNotFound
	}
	return deepCopy(tmpl), nil
}

func (r *InMemoryJobRepository) GetRankJob(_ context.Context, jobID string) (EvertuneRankJob, error) {
	job, ok := r.jobs.Get(jobID)
	if !ok {
		return EvertuneRankJob{}, ErrNotFound
	}
	return deepCopy(job), nil
}

func (r *InMemoryJobRepository) UpsertJobTemplate(_ context.Context, tmpl EvertuneJobTemplate) error {
	r.jobTemplates.Set(tmpl.TemplateID, deepCopy(tmpl))
	return nil
}

func (r *InMemoryJobRepository) UpsertRankJob(_ context.Context, job EvertuneRankJob) error {
	r.jobs.Set(job.JobID, deepCopy(job))
	return nil
}

func (r *InMemoryJobRepository) UpsertSamples(_ context.Context, samples []EvertuneSample) error {
	for _, sample := range samples {
		r.samples.Set(sample.SampleID, deepCopy(sample))
	}
	return nil
}

func (r *InMemoryJobRepository) ListSamplesByJob(_ context.Context, jobID string) ([]EvertuneSample, error) {
	return filterValues(r.samples, func(s EvertuneSample) bool { return s.JobID == jobID }), nil
}

func (r *InMemoryJobRepository) ListSamplesByBatch(_ context.Context, batchID string) ([]EvertuneSample, error) {
	return filterValues(r.samples, func(s EvertuneSample) bool { return s.BatchID == batchID }), nil
}

func filterValues[V any](m cmap.ConcurrentMap[string, V], keep func(V) bool) []V {
	var out []V
	for _, v := range m.Items() {
		if keep(v) {
			out = append(out, deepCopy(v))
		}
	}
	return out
}

func deepCopy[T any](v T) T {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("deepCopy marshal: %v", err))
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		panic(fmt.Sprintf("deepCopy unmarshal: %v", err))
	}
	return out
}
