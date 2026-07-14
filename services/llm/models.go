package llm

import (
	"encoding/json"
	"fmt"
	"sort"
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
	JobID          string         `json:"job_id"`
	TemplateID     string         `json:"template_id"`
	Status         JobStatus      `json:"status"`
	SampleCount    int            `json:"sample_count"`
	SucceededCount int            `json:"succeeded_count"`
	FailedCount    int            `json:"failed_count"`
	Ranking        []ModelRanking `json:"ranking,omitempty"`
	ReportURI      string         `json:"report_uri,omitempty"`
	Error          string         `json:"error,omitempty"`
	RequestedAt    time.Time      `json:"requested_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

type EvertuneSample struct {
	JobID        string         `json:"job_id"`
	SampleID     string         `json:"sample_id"`
	Provider     Provider       `json:"provider"`
	Model        SupportedModel `json:"model"`
	Status       SampleStatus   `json:"status"`
	ResultURI    string         `json:"result_uri,omitempty"`
	RawResponse  string         `json:"raw_response,omitempty"`
	FinishReason string         `json:"finish_reason,omitempty"`
	InputTokens  int32          `json:"input_tokens,omitempty"`
	OutputTokens int32          `json:"output_tokens,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	CompletedAt  time.Time      `json:"completed_at,omitzero"`
}

func (s *EvertuneSample) populateFromResponse(response *GenerateResult) {
	s.RawResponse = response.Text
	s.FinishReason = response.FinishReason
	s.InputTokens = response.InputTokenCount
	s.OutputTokens = response.OutputTokenCount
}

type GenerateRequest struct {
	Prompt          string
	SystemPrompt    string
	Model           string
	MaxOutputTokens int32
	Temperature     float32
}
type GenerateResult struct {
	Text              string
	InputTokenCount   int32
	OutputTokenCount  int32
	ThoughtTokenCount int32
	FinishReason      string
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

// BrandStat is one brand's aggregated standing across a job's samples: its mean
// rank (lower is stronger) and how many samples ranked it.
type BrandStat struct {
	Brand       string  `json:"brand"`
	MeanRank    float64 `json:"mean_rank"`
	SampleCount int     `json:"sample_count"`
}

// ModelRanking is one model's consensus ranking, so results are attributable to
// the LLM that produced them and can be compared across providers.
type ModelRanking struct {
	Provider Provider       `json:"provider"`
	Model    SupportedModel `json:"model"`
	Brands   []BrandStat    `json:"brands"`
}

// AggregateRanking folds a job's succeeded samples into one consensus ranking
// per (provider, model): for each brand, the mean of the ranks it received and
// the number of samples that ranked it. Each model's brands are sorted by mean
// rank ascending (strongest first); models are ordered by provider then model.
// Samples whose raw response no longer parses are skipped.
func AggregateRanking(samples []EvertuneSample) []ModelRanking {
	type modelKey struct {
		provider Provider
		model    SupportedModel
	}
	type accum struct {
		rankSum float64
		count   int
	}
	byModel := map[modelKey]map[string]*accum{}
	for _, sample := range samples {
		if sample.Status != SampleStatusSucceeded {
			continue
		}
		resp, err := parseRankResponse([]byte(sample.RawResponse))
		if err != nil {
			continue
		}
		key := modelKey{provider: sample.Provider, model: sample.Model}
		byBrand := byModel[key]
		if byBrand == nil {
			byBrand = map[string]*accum{}
			byModel[key] = byBrand
		}
		for _, r := range resp.Rankings {
			a := byBrand[r.Brand]
			if a == nil {
				a = &accum{}
				byBrand[r.Brand] = a
			}
			a.rankSum += float64(r.Rank)
			a.count++
		}
	}

	rankings := make([]ModelRanking, 0, len(byModel))
	for key, byBrand := range byModel {
		brands := make([]BrandStat, 0, len(byBrand))
		for brand, a := range byBrand {
			brands = append(brands, BrandStat{
				Brand:       brand,
				MeanRank:    a.rankSum / float64(a.count),
				SampleCount: a.count,
			})
		}
		// Strongest (lowest mean rank) first; break ties by brand for stability.
		sort.Slice(brands, func(i, j int) bool {
			if brands[i].MeanRank != brands[j].MeanRank {
				return brands[i].MeanRank < brands[j].MeanRank
			}
			return brands[i].Brand < brands[j].Brand
		})
		rankings = append(rankings, ModelRanking{
			Provider: key.provider,
			Model:    key.model,
			Brands:   brands,
		})
	}
	// Deterministic model order.
	sort.Slice(rankings, func(i, j int) bool {
		if rankings[i].Provider != rankings[j].Provider {
			return rankings[i].Provider < rankings[j].Provider
		}
		return rankings[i].Model < rankings[j].Model
	})
	return rankings
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
