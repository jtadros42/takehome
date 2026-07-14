package llm

import (
	"encoding/json"
	"fmt"
	"time"
)

type JobStatus string

const (
	JobStatusCreated     JobStatus = "CREATED"
	JobStatusStaged      JobStatus = "STAGED"
	JobStatusSubmitted   JobStatus = "SUBMITTED"
	JobStatusPolling     JobStatus = "POLLING"
	JobStatusFetching    JobStatus = "FETCHING"
	JobStatusAggregating JobStatus = "AGGREGATING"
	JobStatusCompleted   JobStatus = "COMPLETED"
	JobStatusReportReady JobStatus = "REPORT_READY"
	JobStatusFailed      JobStatus = "FAILED"
)

// SampleStatus tracks one individual LLM call through a batch.
type SampleStatus string

const (
	SampleStatusPending   SampleStatus = "PENDING"
	SampleStatusStaged    SampleStatus = "STAGED"
	SampleStatusSubmitted SampleStatus = "SUBMITTED"
	SampleStatusSucceeded SampleStatus = "SUCCEEDED"
	SampleStatusFailed    SampleStatus = "FAILED"
)

type Provider string

const (
	ProviderVertexAI   Provider = "VertexAI"
	ProviderTogetherAI Provider = "TogetherAI"
)

type SupportedModel string

const (
	GeminiFlash SupportedModel = "gemini-2.5-flash"
)

type ModelProvider struct {
	Provider Provider       `json:"provider"`
	Model    SupportedModel `json:"model"`
}

// EvertuneJobTemplate is an abstract definition of one category to track sentiment
// over and which LLMs to compare. One template = one tracked category; the report
// compares how each provider ranks brands within it.
// This will most likely contain a schedule to run the report on a cadence, but for
// the purpose of this assignment you have to explicitly invoke it.
type EvertuneJobTemplate struct {
	TemplateID     string          `json:"template_id"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	ModelProviders []ModelProvider `json:"model_providers"`
	Category       string          `json:"category"`
	Region         string          `json:"region,omitempty"`
	TopN           int             `json:"top_n"`
	SamplesPerCell int             `json:"samples_per_cell"`
}

// EvertuneRankJob represents a real invocation of a workflow
// It is minted with an execution id called JobID. This is used as
// a correlation id to track the lifecycle of a job through various
// workflows
type EvertuneRankJob struct {
	JobID       string    `json:"job_id"`
	TemplateID  string    `json:"template_id"`
	Status      JobStatus `json:"status"`
	SampleCount int       `json:"sample_count"`
	ReportURI   string    `json:"report_uri,omitempty"`
	Error       string    `json:"error,omitempty"`
	RequestedAt time.Time `json:"requested_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type EvertuneSample struct {
	JobID         string         `json:"job_id"`
	SampleID      string         `json:"sample_id"`
	Provider      Provider       `json:"provider"`
	Model         SupportedModel `json:"model"`
	BatchID       string         `json:"batch_id,omitempty"`
	ExternalJobID string         `json:"external_job_id,omitempty"`
	Status        SampleStatus   `json:"status"`
	ResultURI     string         `json:"result_uri,omitempty"`
	RawResponse   string         `json:"raw_response,omitempty"`
	FinishReason  string         `json:"finish_reason,omitempty"`
	InputTokens   int32          `json:"input_tokens,omitempty"`
	OutputTokens  int32          `json:"output_tokens,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	CompletedAt   time.Time      `json:"completed_at,omitzero"`
}

type RankRequest struct {
	Category string `json:"category"`
	Region   string `json:"region,omitempty"`
	TopN     int    `json:"top_n"`
}

type RankResponse struct {
	Category string `json:"category"`
	Region   string `json:"region"`
	Rankings []Rank `json:"rankings"`
	Notes    string `json:"notes"`
}

type Rank struct {
	Rank      int    `json:"rank"`
	Brand     string `json:"brand"`
	Rationale string `json:"rationale"`
}

func parseRankResponse(body []byte) (RankResponse, error) {
	var ranking RankResponse
	if err := json.Unmarshal(body, &ranking); err != nil {
		return RankResponse{}, err
	}
	return ranking, nil
}

// ExpandTemplate instantiates a Job and its Samples given a template.
func ExpandTemplate(tmpl *EvertuneJobTemplate, jobID string, now time.Time) (EvertuneRankJob, []EvertuneSample) {
	samples := make([]EvertuneSample, 0, len(tmpl.ModelProviders)*tmpl.SamplesPerCell)

	for providerIndex, mp := range tmpl.ModelProviders {
		for sampleIndex := range tmpl.SamplesPerCell {
			samples = append(samples, EvertuneSample{
				JobID:     jobID,
				SampleID:  SampleIDFor(jobID, providerIndex, sampleIndex),
				Provider:  mp.Provider,
				Model:     mp.Model,
				Status:    SampleStatusPending,
				CreatedAt: now,
				UpdatedAt: now,
			})
		}
	}

	job := EvertuneRankJob{
		JobID:       jobID,
		TemplateID:  tmpl.TemplateID,
		Status:      JobStatusCreated,
		SampleCount: len(samples),
		RequestedAt: now,
		UpdatedAt:   now,
	}
	return job, samples
}

func SampleIDFor(jobID string, providerIndex, sampleIndex int) string {
	return fmt.Sprintf("%s-p%d-s%d", jobID, providerIndex, sampleIndex)
}
