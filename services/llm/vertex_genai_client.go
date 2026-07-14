package llm

import (
	"context"
	"fmt"

	"google.golang.org/genai"
)

const (
	DefaultTemperatureThreshold = 0.2
	DefaultMaxOutputTokens      = 1024
	DefaultThinkingBudget       = 0
)

type VertexGenAIClient struct {
	client *genai.Client
}

// VertexAIClient interface is defined for mocking/testing retry behavior
type VertexAIClient interface {
	Generate(ctx context.Context, request GenerateRequest) (*GenerateResult, error)
	CountTokens(ctx context.Context, model string, text string) (int32, error)
}

func NewVertexAIGeminiClient(ctx context.Context, projectID string, region string) (*VertexGenAIClient, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  projectID,
		Location: region,
	})
	if err != nil {
		return nil, fmt.Errorf("creating vertex genai client: %w", err)
	}
	return &VertexGenAIClient{
		client: client,
	}, nil
}

func (c *VertexGenAIClient) Generate(ctx context.Context, request GenerateRequest) (*GenerateResult, error) {
	config := &genai.GenerateContentConfig{
		ThinkingConfig: &genai.ThinkingConfig{
			ThinkingBudget: genai.Ptr[int32](DefaultThinkingBudget),
		},
		Temperature:     genai.Ptr[float32](DefaultTemperatureThreshold),
		MaxOutputTokens: coalesceInt32(request.MaxOutputTokens, DefaultMaxOutputTokens),
		CandidateCount:  1,
	}
	if request.SystemPrompt != "" {
		config.SystemInstruction = genai.NewContentFromText(request.SystemPrompt, genai.RoleModel)
	}

	resp, err := c.client.Models.GenerateContent(ctx,
		request.Model,
		genai.Text(request.Prompt),
		config,
	)
	if err != nil {
		return nil, fmt.Errorf("gemini generate: %w", err)
	}
	result := &GenerateResult{Text: resp.Text()}
	if usage := resp.UsageMetadata; usage != nil {
		result.InputTokenCount = usage.PromptTokenCount
		result.OutputTokenCount = usage.CandidatesTokenCount
		result.ThoughtTokenCount = usage.ThoughtsTokenCount
	}
	if len(resp.Candidates) > 0 {
		result.FinishReason = string(resp.Candidates[0].FinishReason)
	}
	return result, nil
}

func (c *VertexGenAIClient) CountTokens(ctx context.Context, model string, text string) (int32, error) {
	resp, err := c.client.Models.CountTokens(ctx, model, genai.Text(text), nil)
	if err != nil {
		return 0, fmt.Errorf("gemini count tokens: %w", err)
	}
	return resp.TotalTokens, nil
}

func coalesceInt32(val int32, fallback int32) int32 {
	if val == 0 {
		return fallback
	}
	return val
}
