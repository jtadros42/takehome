package llm

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type JobStatus string

const (
	JobStatusCreated   JobStatus = "CREATED"
	JobStatusCompleted JobStatus = "COMPLETED"
	JobStatusFailed    JobStatus = "FAILED"
)

// SampleStatus tracks one individual LLM call.
type SampleStatus string

const (
	SampleStatusPending   SampleStatus = "PENDING"
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

// EvertuneJobTemplate defines one category to track and which LLMs to compare.
// One template = one tracked category; a ranking job instantiated from it
// compares how each provider ranks brands within that category.
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

// AggregateRanking produces a sorted list of rankings per (provider, model)
func AggregateRanking(samples []EvertuneSample) []ModelRanking {
	type modelKey struct {
		provider Provider
		model    SupportedModel
	}
	byModel := map[modelKey][]RankResponse{}
	for _, sample := range samples {
		if sample.Status != SampleStatusSucceeded {
			continue
		}
		resp, err := parseRankResponse([]byte(sample.RawResponse))
		if err != nil {
			continue
		}
		key := modelKey{sample.Provider, sample.Model}
		byModel[key] = append(byModel[key], resp)
	}

	rankings := make([]ModelRanking, 0, len(byModel))
	for key, responses := range byModel {
		rankings = append(rankings, ModelRanking{
			Provider: key.provider,
			Model:    key.model,
			Brands:   aggregateBrands(responses),
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

// aggregateBrands folds one model's ranking responses into per-brand mean rank and sample count
func aggregateBrands(responses []RankResponse) []BrandStat {
	type accum struct {
		rankSum float64
		count   int
	}
	byBrand := map[string]*accum{}
	for _, resp := range responses {
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

	brands := make([]BrandStat, 0, len(byBrand))
	for brand, a := range byBrand {
		brands = append(brands, BrandStat{
			Brand:       brand,
			MeanRank:    a.rankSum / float64(a.count),
			SampleCount: a.count,
		})
	}
	sort.Slice(brands, func(i, j int) bool {
		if brands[i].MeanRank != brands[j].MeanRank {
			return brands[i].MeanRank < brands[j].MeanRank
		}
		return brands[i].Brand < brands[j].Brand
	})
	return brands
}

func SampleIDFor(jobID string, providerIndex, sampleIndex int) string {
	return fmt.Sprintf("%s-p%d-s%d", jobID, providerIndex, sampleIndex)
}
