package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed pricing.json
var embeddedPricing []byte

const (
	configPath       = ".plarix.yml"
	commentMarker    = "<!-- plarix-action -->"
	defaultUserAgent = "plarix-action"
)

// Data source modes
const (
	DataSourceMeasured           = "MEASURED"
	DataSourceConfiguredEstimate = "CONFIGURED ESTIMATE"
	DataSourceHeuristicOnly      = "HEURISTIC ONLY"
)

// Config mirrors the small YAML-like assumptions file.
type Config struct {
	Assumptions Assumptions
}

// Assumptions drives cost estimation.
type Assumptions struct {
	RequestsPerDay  int
	AvgInputTokens  int
	AvgOutputTokens int
	Provider        string
	Model           string
}

// PricingFile holds baked-in pricing data.
type PricingFile struct {
	LastUpdated string       `json:"last_updated"`
	Sources     []string     `json:"sources"`
	Models      []ModelPrice `json:"models"`
}

// ModelPrice is per 1M tokens.
type ModelPrice struct {
	Provider         string  `json:"provider"`
	Name             string  `json:"name"`
	InputPerMillion  float64 `json:"input_per_million"`
	OutputPerMillion float64 `json:"output_per_million"`
}

// DiffSignals captures interesting changes from PR diff.
type DiffSignals struct {
	BeforeModels []string
	AfterModels  []string
	BeforeMax    []int
	AfterMax     []int
	BeforeRetry  []int
	AfterRetry   []int
}

// MeasuredUsage represents a single API call from JSONL log.
type MeasuredUsage struct {
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	InputTokens       int    `json:"input_tokens"`
	OutputTokens      int    `json:"output_tokens"`
	CachedInputTokens int    `json:"cached_input_tokens,omitempty"`
	Timestamp         string `json:"timestamp,omitempty"`
}

// MeasuredSummary aggregates measured usage.
type MeasuredSummary struct {
	TotalInputTokens  int
	TotalOutputTokens int
	TotalCost         float64
	CallCount         int
	Models            map[string]int // model -> call count
}

type ghFile struct {
	Filename string `json:"filename"`
	Patch    string `json:"patch"`
}

type ghComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

type ghEvent struct {
	PullRequest struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	Issue struct {
		Number      int `json:"number"`
		PullRequest *struct {
			URL string `json:"url"`
		} `json:"pull_request"`
	} `json:"issue"`
	Number int `json:"number"`
}

type costPair struct {
	PerRequest float64
	Monthly    float64
}

func main() {
	ctx := context.Background()

	pricing, err := findPricing()
	if err != nil {
		fatalf("failed to load pricing: %v", err)
	}

	repo := os.Getenv("GITHUB_REPOSITORY")
	eventPath := os.Getenv("GITHUB_EVENT_PATH")
	token := os.Getenv("GITHUB_TOKEN")

	// Check for measured mode env vars
	measureBasePath := os.Getenv("PLARIX_MEASURE_BASE")
	measureHeadPath := os.Getenv("PLARIX_MEASURE_HEAD")

	if eventPath == "" {
		fatalf("GITHUB_EVENT_PATH is empty")
	}
	if repo == "" {
		fatalf("GITHUB_REPOSITORY is empty")
	}
	if token == "" {
		fatalf("GITHUB_TOKEN is required to read PR diffs")
	}

	prNumber, err := readPRNumber(eventPath)
	if err != nil {
		fatalf("cannot read PR number: %v", err)
	}
	if prNumber == 0 {
		fmt.Println("plarix: not a pull request context, skipping analysis")
		return
	}

	client := newGHClient(token)
	files, err := fetchPRFiles(ctx, client, repo, prNumber)
	if err != nil {
		fatalf("failed to fetch PR files: %v", err)
	}

	signals := extractSignals(files)
	cfg, cfgFound := loadConfig(configPath)

	// Try to load measured data
	var baseMeasured, headMeasured *MeasuredSummary
	if measureBasePath != "" {
		baseMeasured = loadMeasuredUsage(measureBasePath, pricing)
	}
	if measureHeadPath != "" {
		headMeasured = loadMeasuredUsage(measureHeadPath, pricing)
	}

	report := buildReport(reportInput{
		ConfigFound:  cfgFound,
		Config:       cfg.Assumptions,
		Pricing:      pricing,
		Signals:      signals,
		BaseMeasured: baseMeasured,
		HeadMeasured: headMeasured,
	})

	if summaryPath := os.Getenv("GITHUB_STEP_SUMMARY"); summaryPath != "" {
		_ = os.WriteFile(summaryPath, []byte(report), 0o644)
	} else {
		fmt.Println(report)
	}

	if token != "" {
		if err := upsertComment(ctx, client, repo, prNumber, report); err != nil {
			fmt.Fprintf(os.Stderr, "warn: failed to update PR comment: %v\n", err)
		}
	}
}

func findPricing() (PricingFile, error) {
	var p PricingFile
	if err := json.Unmarshal(embeddedPricing, &p); err != nil {
		return PricingFile{}, fmt.Errorf("failed to parse embedded pricing: %w", err)
	}
	return p, nil
}

func loadConfig(path string) (Config, bool) {
	// Default to cheap baseline model - but these are ONLY used if config exists
	cfg := Config{Assumptions: Assumptions{
		RequestsPerDay:  10000,
		AvgInputTokens:  800,
		AvgOutputTokens: 400,
		Provider:        "openai",
		Model:           "gpt-4o-mini",
	}}

	f, err := os.Open(path)
	if err != nil {
		return cfg, false
	}
	defer f.Close()

	var current string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasSuffix(line, ":") {
			current = strings.TrimSuffix(line, ":")
			continue
		}
		if current != "assumptions" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
		switch key {
		case "requests_per_day":
			if v, err := strconv.Atoi(val); err == nil {
				cfg.Assumptions.RequestsPerDay = v
			}
		case "avg_input_tokens":
			if v, err := strconv.Atoi(val); err == nil {
				cfg.Assumptions.AvgInputTokens = v
			}
		case "avg_output_tokens":
			if v, err := strconv.Atoi(val); err == nil {
				cfg.Assumptions.AvgOutputTokens = v
			}
		case "provider":
			cfg.Assumptions.Provider = strings.ToLower(val)
		case "model":
			cfg.Assumptions.Model = val
		}
	}
	return cfg, true
}

func loadMeasuredUsage(path string, pricing PricingFile) *MeasuredSummary {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: cannot open measured file %s: %v\n", path, err)
		return nil
	}
	defer f.Close()

	summary := &MeasuredSummary{Models: make(map[string]int)}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var u MeasuredUsage
		if err := json.Unmarshal([]byte(line), &u); err != nil {
			continue // skip malformed lines
		}
		summary.TotalInputTokens += u.InputTokens
		summary.TotalOutputTokens += u.OutputTokens
		summary.CallCount++
		summary.Models[u.Model]++

		// Compute cost for this call
		price, _ := priceFor(pricing, u.Provider, u.Model)
		callCost := (float64(u.InputTokens)*price.InputPerMillion + float64(u.OutputTokens)*price.OutputPerMillion) / 1_000_000
		summary.TotalCost += callCost
	}

	if summary.CallCount == 0 {
		return nil
	}
	return summary
}

func readPRNumber(eventPath string) (int, error) {
	data, err := os.ReadFile(eventPath)
	if err != nil {
		return 0, err
	}
	var ev ghEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return 0, err
	}

	if ev.PullRequest.Number != 0 {
		return ev.PullRequest.Number, nil
	}
	if ev.Issue.Number != 0 && ev.Issue.PullRequest != nil {
		return ev.Issue.Number, nil
	}
	if ev.Number != 0 {
		return ev.Number, nil
	}
	if ref := os.Getenv("GITHUB_REF"); strings.HasPrefix(ref, "refs/pull/") {
		parts := strings.Split(ref, "/")
		if len(parts) >= 3 {
			if n, err := strconv.Atoi(parts[2]); err == nil && n > 0 {
				return n, nil
			}
		}
	}
	return 0, nil
}

func newGHClient(token string) *http.Client {
	return &http.Client{Timeout: 15 * time.Second, Transport: &authTransport{token: token}}
}

type authTransport struct {
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", defaultUserAgent)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func fetchPRFiles(ctx context.Context, client *http.Client, repo string, prNumber int) ([]ghFile, error) {
	var all []ghFile
	for page := 1; page <= 10; page++ {
		url := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d/files?per_page=100&page=%d", repo, prNumber, page)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("github api: %s", resp.Status)
		}
		var files []ghFile
		if err := json.Unmarshal(body, &files); err != nil {
			return nil, err
		}
		all = append(all, files...)
		if len(files) < 100 {
			break
		}
	}
	return all, nil
}

var (
	modelPattern     = regexp.MustCompile(`(?i)\b(gpt-[\w.-]+|claude-[\w.-]+)\b`)
	maxTokensPattern = regexp.MustCompile(`(?i)max[_-]?tokens\s*[:=]\s*([0-9]+)`)
	retryPattern     = regexp.MustCompile(`(?i)(retries|maxRetries|retry\s*count|retry_limit)\s*[:=]\s*([0-9]+)`)
)

func extractSignals(files []ghFile) DiffSignals {
	var s DiffSignals
	for _, f := range files {
		if f.Patch == "" {
			continue
		}
		scanner := bufio.NewScanner(strings.NewReader(f.Patch))
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "@@") {
				continue
			}
			var targetModels *[]string
			var targetMax *[]int
			var targetRetry *[]int
			if strings.HasPrefix(line, "-") {
				targetModels = &s.BeforeModels
				targetMax = &s.BeforeMax
				targetRetry = &s.BeforeRetry
			} else if strings.HasPrefix(line, "+") {
				targetModels = &s.AfterModels
				targetMax = &s.AfterMax
				targetRetry = &s.AfterRetry
			} else {
				continue
			}

			for _, m := range modelPattern.FindAllString(line, -1) {
				*targetModels = append(*targetModels, m)
			}
			for _, m := range maxTokensPattern.FindAllStringSubmatch(line, -1) {
				if v, err := strconv.Atoi(m[1]); err == nil {
					*targetMax = append(*targetMax, v)
				}
			}
			for _, m := range retryPattern.FindAllStringSubmatch(line, -1) {
				if v, err := strconv.Atoi(m[2]); err == nil {
					*targetRetry = append(*targetRetry, v)
				}
			}
		}
	}
	return s
}

func computeEstimate(a Assumptions, pricing PricingFile, model string) (costPair, bool) {
	price, found := priceFor(pricing, a.Provider, model)
	perRequest := (float64(a.AvgInputTokens)*price.InputPerMillion + float64(a.AvgOutputTokens)*price.OutputPerMillion) / 1_000_000
	monthly := perRequest * float64(a.RequestsPerDay) * 30
	return costPair{PerRequest: perRequest, Monthly: monthly}, found
}

func priceFor(pricing PricingFile, provider, model string) (ModelPrice, bool) {
	provider = strings.ToLower(provider)
	for _, m := range pricing.Models {
		if strings.EqualFold(m.Provider, provider) && strings.EqualFold(m.Name, model) {
			return m, true
		}
	}
	return ModelPrice{Provider: provider, Name: model}, false
}

type reportInput struct {
	ConfigFound  bool
	Config       Assumptions
	Pricing      PricingFile
	Signals      DiffSignals
	BaseMeasured *MeasuredSummary
	HeadMeasured *MeasuredSummary
}

func buildReport(in reportInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", commentMarker)
	fmt.Fprintf(&b, "## 📊 Plarix LLM Cost Analysis\n\n")

	// Determine data source mode
	hasMeasured := in.BaseMeasured != nil || in.HeadMeasured != nil
	hasConfig := in.ConfigFound
	hasSignals := len(in.Signals.BeforeModels)+len(in.Signals.AfterModels)+len(in.Signals.BeforeMax)+len(in.Signals.AfterMax)+len(in.Signals.BeforeRetry)+len(in.Signals.AfterRetry) > 0

	// Definitions
	fmt.Fprintf(&b, "**Definitions:**\n")
	fmt.Fprintf(&b, "- **Before** = base branch (merge target)\n")
	fmt.Fprintf(&b, "- **After** = PR head (this PR's code)\n\n")

	// Data source
	var dataSource string
	if hasMeasured {
		dataSource = DataSourceMeasured
	} else if hasConfig {
		dataSource = DataSourceConfiguredEstimate
	} else {
		dataSource = DataSourceHeuristicOnly
	}
	fmt.Fprintf(&b, "**Data source:** `%s`\n\n", dataSource)

	// Pricing info
	fmt.Fprintf(&b, "_Pricing: %s · Sources: %s_\n\n", safeValue(in.Pricing.LastUpdated, "unknown"), strings.Join(in.Pricing.Sources, ", "))

	// MEASURED MODE - the "wow" feature
	if hasMeasured {
		buildMeasuredReport(&b, in)
		return b.String()
	}

	// CONFIGURED ESTIMATE MODE
	if hasConfig {
		buildConfiguredEstimateReport(&b, in, hasSignals)
		return b.String()
	}

	// HEURISTIC ONLY MODE - no config, no measured data
	buildHeuristicOnlyReport(&b, in, hasSignals)
	return b.String()
}

func buildMeasuredReport(b *strings.Builder, in reportInput) {
	fmt.Fprintf(b, "### ✅ Measured Token Usage (from CI test runs)\n\n")

	if in.BaseMeasured != nil && in.HeadMeasured != nil {
		// Before/After comparison
		fmt.Fprintf(b, "| | Calls | Input Tokens | Output Tokens | Total Cost |\n")
		fmt.Fprintf(b, "|---|---:|---:|---:|---:|\n")
		fmt.Fprintf(b, "| Before | %d | %s | %s | $%.4f |\n",
			in.BaseMeasured.CallCount,
			formatInt(in.BaseMeasured.TotalInputTokens),
			formatInt(in.BaseMeasured.TotalOutputTokens),
			in.BaseMeasured.TotalCost)
		fmt.Fprintf(b, "| After | %d | %s | %s | $%.4f |\n\n",
			in.HeadMeasured.CallCount,
			formatInt(in.HeadMeasured.TotalInputTokens),
			formatInt(in.HeadMeasured.TotalOutputTokens),
			in.HeadMeasured.TotalCost)

		// Delta
		delta := in.HeadMeasured.TotalCost - in.BaseMeasured.TotalCost
		deltaPercent := float64(0)
		if in.BaseMeasured.TotalCost > 0 {
			deltaPercent = (delta / in.BaseMeasured.TotalCost) * 100
		}
		sign := "+"
		if delta < 0 {
			sign = ""
		}
		fmt.Fprintf(b, "**Delta:** %s$%.4f (%s%.1f%%)\n\n", sign, delta, sign, deltaPercent)

		// Trend bar
		maxCost := in.BaseMeasured.TotalCost
		if in.HeadMeasured.TotalCost > maxCost {
			maxCost = in.HeadMeasured.TotalCost
		}
		if maxCost == 0 {
			maxCost = 1
		}
		fmt.Fprintf(b, "```\n")
		fmt.Fprintf(b, "Before |%s $%.4f\n", bar(in.BaseMeasured.TotalCost, maxCost), in.BaseMeasured.TotalCost)
		fmt.Fprintf(b, "After  |%s $%.4f\n", bar(in.HeadMeasured.TotalCost, maxCost), in.HeadMeasured.TotalCost)
		fmt.Fprintf(b, "```\n\n")

		// Models used
		allModels := mergeModelCounts(in.BaseMeasured.Models, in.HeadMeasured.Models)
		if len(allModels) > 0 {
			fmt.Fprintf(b, "**Models used:** %s\n\n", strings.Join(allModels, ", "))
		}
	} else if in.HeadMeasured != nil {
		// Only head measured
		fmt.Fprintf(b, "| Calls | Input Tokens | Output Tokens | Total Cost |\n")
		fmt.Fprintf(b, "|---:|---:|---:|---:|\n")
		fmt.Fprintf(b, "| %d | %s | %s | $%.4f |\n\n",
			in.HeadMeasured.CallCount,
			formatInt(in.HeadMeasured.TotalInputTokens),
			formatInt(in.HeadMeasured.TotalOutputTokens),
			in.HeadMeasured.TotalCost)

		if len(in.HeadMeasured.Models) > 0 {
			models := make([]string, 0, len(in.HeadMeasured.Models))
			for m := range in.HeadMeasured.Models {
				models = append(models, m)
			}
			fmt.Fprintf(b, "**Models used:** %s\n\n", strings.Join(models, ", "))
		}
		fmt.Fprintf(b, "_Note: Only HEAD measurement available. Set `PLARIX_MEASURE_BASE` to enable before/after comparison._\n\n")
	} else if in.BaseMeasured != nil {
		// Only base measured
		fmt.Fprintf(b, "| Calls | Input Tokens | Output Tokens | Total Cost |\n")
		fmt.Fprintf(b, "|---:|---:|---:|---:|\n")
		fmt.Fprintf(b, "| %d | %s | %s | $%.4f |\n\n",
			in.BaseMeasured.CallCount,
			formatInt(in.BaseMeasured.TotalInputTokens),
			formatInt(in.BaseMeasured.TotalOutputTokens),
			in.BaseMeasured.TotalCost)

		fmt.Fprintf(b, "_Note: Only BASE measurement available. Set `PLARIX_MEASURE_HEAD` to enable before/after comparison._\n\n")
	}

	// Also show diff signals if any
	if hasAnySignals(in.Signals) {
		fmt.Fprintf(b, "---\n\n")
		writeDiffSignals(b, in.Signals)
	}
}

func buildConfiguredEstimateReport(b *strings.Builder, in reportInput, hasSignals bool) {
	fmt.Fprintf(b, "### 📋 Configured Estimate (from .plarix.yml)\n\n")

	// Show assumptions explicitly
	fmt.Fprintf(b, "**Assumptions from config:**\n")
	fmt.Fprintf(b, "- Requests/day: %d\n", in.Config.RequestsPerDay)
	fmt.Fprintf(b, "- Avg input tokens: %d\n", in.Config.AvgInputTokens)
	fmt.Fprintf(b, "- Avg output tokens: %d\n", in.Config.AvgOutputTokens)
	fmt.Fprintf(b, "- Provider: %s\n", in.Config.Provider)
	fmt.Fprintf(b, "- Model: %s\n\n", in.Config.Model)

	beforeModel := firstOrDefault(in.Signals.BeforeModels, in.Config.Model)
	afterModel := firstOrDefault(in.Signals.AfterModels, in.Config.Model)

	beforeCost, beforeFound := computeEstimate(in.Config, in.Pricing, beforeModel)
	afterCost, afterFound := computeEstimate(in.Config, in.Pricing, afterModel)

	// Show formula
	fmt.Fprintf(b, "**Formula:** `cost = (input_tokens × input_price + output_tokens × output_price) / 1M × requests/day × 30`\n\n")

	// Cost table
	fmt.Fprintf(b, "| | Model | Est. per request | Est. monthly |\n")
	fmt.Fprintf(b, "|---|---|---:|---:|\n")
	fmt.Fprintf(b, "| Before | %s | $%.4f | $%.2f |\n", beforeModel, beforeCost.PerRequest, beforeCost.Monthly)
	fmt.Fprintf(b, "| After | %s | $%.4f | $%.2f |\n\n", afterModel, afterCost.PerRequest, afterCost.Monthly)

	// Trend bar
	maxMonthly := beforeCost.Monthly
	if afterCost.Monthly > maxMonthly {
		maxMonthly = afterCost.Monthly
	}
	if maxMonthly == 0 {
		maxMonthly = 1
	}
	fmt.Fprintf(b, "```\n")
	fmt.Fprintf(b, "Before |%s $%.2f\n", bar(beforeCost.Monthly, maxMonthly), beforeCost.Monthly)
	fmt.Fprintf(b, "After  |%s $%.2f\n", bar(afterCost.Monthly, maxMonthly), afterCost.Monthly)
	fmt.Fprintf(b, "```\n\n")

	if !beforeFound || !afterFound {
		fmt.Fprintf(b, "_⚠️ Pricing not found for one or more models; costs may be $0.00._\n\n")
	}

	fmt.Fprintf(b, "_⚠️ These are **estimates** based on configured assumptions, not actual usage._\n\n")

	// Diff signals
	if hasSignals {
		fmt.Fprintf(b, "---\n\n")
		writeDiffSignals(b, in.Signals)
	}
}

func buildHeuristicOnlyReport(b *strings.Builder, in reportInput, hasSignals bool) {
	fmt.Fprintf(b, "### ⚠️ Cannot Compute Real Cost\n\n")
	fmt.Fprintf(b, "**No `.plarix.yml` config and no measured token logs found.**\n\n")
	fmt.Fprintf(b, "Without configuration or measurements, we cannot estimate costs reliably.\n\n")

	if hasSignals {
		fmt.Fprintf(b, "---\n\n")
		fmt.Fprintf(b, "### 🔍 Detected PR Signals (diff-based heuristics)\n\n")
		writeDiffSignals(b, in.Signals)
	} else {
		fmt.Fprintf(b, "No LLM-cost-relevant changes detected in this PR.\n\n")
	}

	// How to enable
	fmt.Fprintf(b, "---\n\n")
	fmt.Fprintf(b, "### 📖 How to Enable Real Reporting\n\n")
	fmt.Fprintf(b, "**Option 1: Configured Estimate** (quick setup)\n")
	fmt.Fprintf(b, "Create `.plarix.yml` in your repo root:\n")
	fmt.Fprintf(b, "```yaml\n")
	fmt.Fprintf(b, "assumptions:\n")
	fmt.Fprintf(b, "  requests_per_day: 10000\n")
	fmt.Fprintf(b, "  avg_input_tokens: 800\n")
	fmt.Fprintf(b, "  avg_output_tokens: 400\n")
	fmt.Fprintf(b, "  provider: \"openai\"\n")
	fmt.Fprintf(b, "  model: \"gpt-4o-mini\"\n")
	fmt.Fprintf(b, "```\n\n")
	fmt.Fprintf(b, "**Option 2: Measured Mode** (most accurate)\n")
	fmt.Fprintf(b, "Instrument your tests to log token usage to JSONL files, then set:\n")
	fmt.Fprintf(b, "- `PLARIX_MEASURE_BASE` = path to base branch usage log\n")
	fmt.Fprintf(b, "- `PLARIX_MEASURE_HEAD` = path to PR head usage log\n\n")
	fmt.Fprintf(b, "See [plarix-action README](https://github.com/aegix-ai/plarix-action) for detailed setup.\n")
}

func writeDiffSignals(b *strings.Builder, s DiffSignals) {
	fmt.Fprintf(b, "**Observed changes (diff-based heuristics):**\n")
	if len(s.BeforeModels) > 0 || len(s.AfterModels) > 0 {
		fmt.Fprintf(b, "- **Models:** %s → %s\n", listOrPlaceholder(s.BeforeModels), listOrPlaceholder(s.AfterModels))
	}
	if len(s.BeforeMax) > 0 || len(s.AfterMax) > 0 {
		fmt.Fprintf(b, "- **max_tokens:** %s → %s\n", intsOrDash(s.BeforeMax), intsOrDash(s.AfterMax))
	}
	if len(s.BeforeRetry) > 0 || len(s.AfterRetry) > 0 {
		fmt.Fprintf(b, "- **retries:** %s → %s\n", intsOrDash(s.BeforeRetry), intsOrDash(s.AfterRetry))
	}
	fmt.Fprintf(b, "\n")
}

func hasAnySignals(s DiffSignals) bool {
	return len(s.BeforeModels)+len(s.AfterModels)+len(s.BeforeMax)+len(s.AfterMax)+len(s.BeforeRetry)+len(s.AfterRetry) > 0
}

func bar(value, max float64) string {
	width := 22
	filled := int((value / max) * float64(width))
	if filled < 1 && value > 0 {
		filled = 1
	}
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("·", width-filled)
}

func formatInt(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return strconv.Itoa(n)
}

func mergeModelCounts(a, b map[string]int) []string {
	seen := make(map[string]bool)
	var result []string
	for m := range a {
		if !seen[m] {
			seen[m] = true
			result = append(result, m)
		}
	}
	for m := range b {
		if !seen[m] {
			seen[m] = true
			result = append(result, m)
		}
	}
	return result
}

func listOrPlaceholder(values []string) string {
	if len(values) == 0 {
		return "—"
	}
	return strings.Join(uniqueStrings(values), ", ")
}

func intsOrDash(values []int) string {
	if len(values) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(values))
	for _, v := range uniqueInts(values) {
		parts = append(parts, strconv.Itoa(v))
	}
	return strings.Join(parts, ", ")
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func uniqueInts(in []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func safeValue(val, fallback string) string {
	if strings.TrimSpace(val) == "" {
		return fallback
	}
	return val
}

func firstOrDefault(list []string, fallback string) string {
	if len(list) == 0 {
		return fallback
	}
	return list[0]
}

func upsertComment(ctx context.Context, client *http.Client, repo string, prNumber int, body string) error {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return fmt.Errorf("invalid repo: %s", repo)
	}
	existingID, err := findExistingComment(ctx, client, owner, name, prNumber)
	if err != nil {
		return err
	}
	if existingID == 0 {
		return createComment(ctx, client, owner, name, prNumber, body)
	}
	return updateComment(ctx, client, owner, name, existingID, body)
}

func findExistingComment(ctx context.Context, client *http.Client, owner, repo string, prNumber int) (int64, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d/comments?per_page=100", owner, repo, prNumber)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("github api: %s", resp.Status)
	}
	var comments []ghComment
	if err := json.NewDecoder(resp.Body).Decode(&comments); err != nil {
		return 0, err
	}
	for _, c := range comments {
		if strings.Contains(c.Body, commentMarker) {
			return c.ID, nil
		}
	}
	return 0, nil
}

func createComment(ctx context.Context, client *http.Client, owner, repo string, prNumber int, body string) error {
	payload := map[string]string{"body": body}
	buf, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d/comments", owner, repo, prNumber)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("create comment: %s", resp.Status)
	}
	return nil
}

func updateComment(ctx context.Context, client *http.Client, owner, repo string, id int64, body string) error {
	payload := map[string]string{"body": body}
	buf, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/comments/%d", owner, repo, id)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(buf))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("update comment: %s", resp.Status)
	}
	return nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
