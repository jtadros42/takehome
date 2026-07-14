//go:build integration

package llm

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jtadros42/takehome/services/llm/resources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	TestProjectID = "evertune-tests"
	TestRegion    = "us-central1"

	// Minimum content size for explicit (and implicit) context caching on
	// Gemini 2.5 Flash. Below this, Caches.Create is rejected.
	GeminiFlashCacheMinTokens = 1024
)

// TestMain fails fast with a clear message if credentials are missing, rather
// than letting every test error on its own first API call.
func TestMain(m *testing.M) {
	if _, err := NewVertexAIGeminiClient(context.Background(), TestProjectID, TestRegion); err != nil {
		println("integration tests require Application Default Credentials:")
		println("  gcloud auth application-default login")
		println("error:", err.Error())
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// newTestClient builds a client and a per-test context with a timeout.
func newTestClient(t *testing.T, timeout time.Duration) (*VertexGenAIClient, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	client, err := NewVertexAIGeminiClient(ctx, TestProjectID, TestRegion)
	require.NoError(t, err, "creating client")
	return client, ctx
}

func TestVertexGenAIClient_Generate(t *testing.T) {
	client, ctx := newTestClient(t, 60*time.Second)

	result, err := client.Generate(ctx, GenerateRequest{
		Prompt: "What are the top 10 luxury car brands",
		Model:  "gemini-2.5-flash",
	})
	require.NoError(t, err, "generate")
	require.NotNil(t, result)
	assert.NotZero(t, result.InputTokenCount, "input token count")
	assert.NotEmpty(t, result.Text, "response text is empty, finish reason %q", result.FinishReason)
	assert.Equal(t, "STOP", result.FinishReason, "MAX_TOKENS here means truncation")
	t.Logf("response:\n%s", result.Text)
}

func TestVertexGenAIClient_Generate_RankerSystemPrompt(t *testing.T) {
	systemPrompt, err := resources.SystemPrompt("ranker")
	require.NoError(t, err, "loading ranker system prompt")

	client, ctx := newTestClient(t, 120*time.Second)

	testCases := []RankRequest{
		{
			Category: "luxury car brands",
			TopN:     10,
			Region:   "US",
		},
		{
			Category: "best chocolate snacks",
			TopN:     7,
			Region:   "US",
		},
		{
			Category: "best llm product for graphics",
			TopN:     5,
			Region:   "US",
		},
		{
			Category: "most reliable cars",
			TopN:     5,
			Region:   "US",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.Category, func(t *testing.T) {
			userPrompt, err := json.Marshal(testCase)
			require.NoError(t, err, "error json marshalling prompt")

			result, err := client.Generate(ctx, GenerateRequest{
				SystemPrompt: systemPrompt,
				Prompt:       string(userPrompt),
				Model:        "gemini-2.5-flash",
			})
			require.NoError(t, err, "error calling llm")
			require.NotNil(t, result)
			require.Equal(t, "STOP", result.FinishReason, "MAX_TOKENS here means truncation")
			require.NotEmpty(t, result.Text, "response text")

			rankResponse, err := parseRankResponse([]byte(result.Text))
			require.NoError(t, err, "response was not valid JSON per the ranker contract:\n%s", result.Text)

			assert.Equal(t, testCase.Category, rankResponse.Category)
			assert.Len(t, rankResponse.Rankings, testCase.TopN, "expected exactly top_n brands")
			for i, r := range rankResponse.Rankings {
				assert.Equal(t, i+1, r.Rank, "ranks should be contiguous from 1")
				assert.NotEmpty(t, r.Brand, "brand at rank %d", i+1)
			}
			t.Logf("ranking:\n%s", result.Text)
		})
	}
}

// TestRankerSystemPrompt_TokenCount reports the exact token count of the ranker
func TestRankerSystemPrompt_TokenCount(t *testing.T) {
	systemPrompt, err := resources.SystemPrompt("ranker")
	require.NoError(t, err, "loading ranker system prompt")
	client, ctx := newTestClient(t, 60*time.Second)
	tokens, err := client.CountTokens(ctx, "gemini-2.5-flash", systemPrompt)
	require.NoError(t, err, "counting tokens")
	require.NotZero(t, tokens)
	t.Logf("ranker system prompt: %d tokens (cache floor is %d)", tokens, GeminiFlashCacheMinTokens)
}
