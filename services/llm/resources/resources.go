package resources

import (
	"embed"
	"fmt"
)

//go:embed system_prompts/*.md
var systemPrompts embed.FS

// SystemPrompt returns the embedded system prompt with the given name
// (without the .md extension), e.g. SystemPrompt("ranker").
func SystemPrompt(name string) (string, error) {
	data, err := systemPrompts.ReadFile("system_prompts/" + name + ".md")
	if err != nil {
		return "", fmt.Errorf("loading system prompt %q: %w", name, err)
	}
	return string(data), nil
}
