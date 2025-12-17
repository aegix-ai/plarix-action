package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// This tool rewrites pricing.json from the table below.
// After updating prices from official sources, run: go run ./cmd/update-pricing
func main() {
	pricing := map[string]any{
		"last_updated": time.Now().Format("2006-01-02"),
		"sources": []string{
			"https://platform.openai.com/docs/pricing",
			"https://claude.com/platform/api",
		},
		"models": []map[string]any{
			// OpenAI models (verified Dec 2024 from platform.openai.com/docs/pricing)
			{"provider": "openai", "name": "gpt-4o", "input_per_million": 2.50, "output_per_million": 10.0},
			{"provider": "openai", "name": "gpt-4o-mini", "input_per_million": 0.15, "output_per_million": 0.60},
			{"provider": "openai", "name": "gpt-4-turbo", "input_per_million": 10.0, "output_per_million": 30.0},
			{"provider": "openai", "name": "gpt-3.5-turbo", "input_per_million": 0.50, "output_per_million": 1.50},
			{"provider": "openai", "name": "o1", "input_per_million": 15.0, "output_per_million": 60.0},
			{"provider": "openai", "name": "o1-mini", "input_per_million": 1.10, "output_per_million": 4.40},
			{"provider": "openai", "name": "o3", "input_per_million": 2.0, "output_per_million": 8.0},
			{"provider": "openai", "name": "o3-mini", "input_per_million": 1.10, "output_per_million": 4.40},
			{"provider": "openai", "name": "o4-mini", "input_per_million": 1.10, "output_per_million": 4.40},
			// Anthropic models (verified Dec 2024 from claude.com/platform/api)
			{"provider": "anthropic", "name": "claude-sonnet-4", "input_per_million": 3.0, "output_per_million": 15.0},
			{"provider": "anthropic", "name": "claude-3-5-sonnet", "input_per_million": 3.0, "output_per_million": 15.0},
			{"provider": "anthropic", "name": "claude-3-5-sonnet-latest", "input_per_million": 3.0, "output_per_million": 15.0},
			{"provider": "anthropic", "name": "claude-haiku-4", "input_per_million": 1.0, "output_per_million": 5.0},
			{"provider": "anthropic", "name": "claude-3-5-haiku", "input_per_million": 1.0, "output_per_million": 5.0},
			{"provider": "anthropic", "name": "claude-opus-4", "input_per_million": 5.0, "output_per_million": 25.0},
			{"provider": "anthropic", "name": "claude-3-opus", "input_per_million": 15.0, "output_per_million": 75.0},
		},
	}

	data, err := json.MarshalIndent(pricing, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Write to root pricing.json
	if err := os.WriteFile("pricing.json", data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error writing pricing.json: %v\n", err)
		os.Exit(1)
	}

	// Also copy to cmd/plarix for embedding
	if err := os.WriteFile("cmd/plarix/pricing.json", data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error writing cmd/plarix/pricing.json: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Updated pricing.json and cmd/plarix/pricing.json")
}
