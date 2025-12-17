package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type PricingFile struct {
	LastUpdated string       `json:"last_updated"`
	Sources     []string     `json:"sources"`
	Models      []ModelPrice `json:"models"`
}

type ModelPrice struct {
	Provider         string  `json:"provider"`
	Name             string  `json:"name"`
	InputPerMillion  float64 `json:"input_per_million"`
	OutputPerMillion float64 `json:"output_per_million"`
}

func main() {
	models := []ModelPrice{
		{Provider: "openai", Name: "gpt-4o", InputPerMillion: 5.00, OutputPerMillion: 15.00},
		{Provider: "openai", Name: "gpt-4o-mini", InputPerMillion: 0.15, OutputPerMillion: 0.60},
		{Provider: "openai", Name: "gpt-3.5-turbo", InputPerMillion: 0.50, OutputPerMillion: 1.50},
		{Provider: "anthropic", Name: "claude-3-5-sonnet", InputPerMillion: 3.00, OutputPerMillion: 15.00},
		{Provider: "anthropic", Name: "claude-3-5-sonnet-latest", InputPerMillion: 3.00, OutputPerMillion: 15.00},
		{Provider: "anthropic", Name: "claude-3-opus", InputPerMillion: 15.00, OutputPerMillion: 75.00},
	}

	pricing := PricingFile{
		LastUpdated: time.Now().Format("2006-01-02"),
		Sources: []string{
			"https://openai.com/pricing",
			"https://www.anthropic.com/pricing",
		},
		Models: models,
	}

	buf, err := json.MarshalIndent(pricing, "", "  ")
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile("pricing.json", buf, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("pricing.json updated with %d models (last_updated=%s)\n", len(models), pricing.LastUpdated)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "update-pricing: %v\n", err)
	os.Exit(1)
}
