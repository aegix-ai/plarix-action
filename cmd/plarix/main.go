package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	configPath       = ".plarix.yml"
	pricingPath      = "pricing.json"
	commentMarker    = "<!-- plarix-action -->"
	defaultUserAgent = "plarix-action"
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

// DiffSignals captures interesting changes.
type DiffSignals struct {
	BeforeModels []string
	AfterModels  []string
	BeforeMax    []int
	AfterMax     []int
	BeforeRetry  []int
	AfterRetry   []int
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
}

func main() {
	ctx := context.Background()

	cfg, cfgFound := loadConfig(configPath)
	pricing, err := loadPricing(pricingPath)
	if err != nil {
		fatalf("failed to load pricing: %v", err)
	}

	repo := os.Getenv("GITHUB_REPOSITORY")
	eventPath := os.Getenv("GITHUB_EVENT_PATH")
	token := os.Getenv("GITHUB_TOKEN")
	if eventPath == "" {
		fatalf("GITHUB_EVENT_PATH is empty")
	}
	if token == "" {
		fatalf("GITHUB_TOKEN is empty; required to read PR diffs")
	}
	prNumber, err := readPRNumber(eventPath)
	if err != nil {
		fatalf("cannot read PR number: %v", err)
	}
	if repo == "" {
		fatalf("GITHUB_REPOSITORY is empty")
	}

	client := newGHClient(token)
	files, err := fetchPRFiles(ctx, client, repo, prNumber)
	if err != nil {
		fatalf("failed to fetch PR files: %v", err)
	}

	signals := extractSignals(files)

	beforeModel := firstOrDefault(signals.BeforeModels, cfg.Assumptions.Model)
	afterModel := firstOrDefault(signals.AfterModels, cfg.Assumptions.Model)

	beforeCost, beforeFound := computeEstimate(cfg.Assumptions, pricing, beforeModel)
	afterCost, afterFound := computeEstimate(cfg.Assumptions, pricing, afterModel)

	report := buildReport(reportInput{
		ConfigFound: cfgFound,
		Config:      cfg.Assumptions,
		Pricing:     pricing,
		Signals:     signals,
		BeforeModel: beforeModel,
		AfterModel:  afterModel,
		BeforeCost:  beforeCost,
		AfterCost:   afterCost,
		PricingHits: pricingHit{BeforeFound: beforeFound, AfterFound: afterFound},
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

func loadConfig(path string) (Config, bool) {
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

func loadPricing(path string) (PricingFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PricingFile{}, err
	}
	var p PricingFile
	if err := json.Unmarshal(data, &p); err != nil {
		return PricingFile{}, err
	}
	return p, nil
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
	if ev.PullRequest.Number == 0 {
		return 0, errors.New("pull_request.number missing")
	}
	return ev.PullRequest.Number, nil
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
	retryPattern     = regexp.MustCompile(`(?i)(retries|maxRetries|retry\s*count|retry_limit)\s*[:=]\s*([0-9]+)`) // limited heuristics
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
			target := &s.AfterModels
			targetMax := &s.AfterMax
			targetRetry := &s.AfterRetry
			if strings.HasPrefix(line, "-") {
				target = &s.BeforeModels
				targetMax = &s.BeforeMax
				targetRetry = &s.BeforeRetry
			} else if !strings.HasPrefix(line, "+") {
				continue
			}
			for _, m := range modelPattern.FindAllString(line, -1) {
				*target = append(*target, m)
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
	model = strings.ToLower(model)
	found := false
	var fallback ModelPrice
	for _, m := range pricing.Models {
		if strings.EqualFold(m.Provider, provider) && strings.EqualFold(m.Name, model) {
			return m, true
		}
		if !found && strings.EqualFold(m.Provider, provider) {
			fallback = m
			found = true
		}
	}
	if found {
		return fallback, false
	}
	if len(pricing.Models) > 0 {
		return pricing.Models[0], false
	}
	return ModelPrice{Provider: provider, Name: model}, false
}

type reportInput struct {
	ConfigFound bool
	Config      Assumptions
	Pricing     PricingFile
	Signals     DiffSignals
	BeforeModel string
	AfterModel  string
	BeforeCost  costPair
	AfterCost   costPair
	PricingHits pricingHit
}

type costPair struct {
	PerRequest float64
	Monthly    float64
}

type pricingHit struct {
	BeforeFound bool
	AfterFound  bool
}

func buildReport(in reportInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", commentMarker)
	fmt.Fprintf(&b, "### LLM cost check\n")
	if !in.ConfigFound {
		fmt.Fprintf(&b, "Using built-in defaults (no .plarix.yml found).\n\n")
	}

	fmt.Fprintf(&b, "Pricing: %s (sources: %s)\n\n", safeValue(in.Pricing.LastUpdated, "unknown"), strings.Join(in.Pricing.Sources, ", "))

	fmt.Fprintf(&b, "Assumptions: %d req/day | %d in tokens | %d out tokens | %s/%s\n\n", in.Config.RequestsPerDay, in.Config.AvgInputTokens, in.Config.AvgOutputTokens, in.Config.Provider, in.AfterModel)

	fmt.Fprintf(&b, "| | Model | Est. per request | Est. monthly |\n")
	fmt.Fprintf(&b, "|---|---|---|---|\n")
	fmt.Fprintf(&b, "| Before | %s | $%.4f | $%.2f |\n", in.BeforeModel, in.BeforeCost.PerRequest, in.BeforeCost.Monthly)
	fmt.Fprintf(&b, "| After  | %s | $%.4f | $%.2f |\n\n", in.AfterModel, in.AfterCost.PerRequest, in.AfterCost.Monthly)

	if !in.PricingHits.BeforeFound || !in.PricingHits.AfterFound {
		missing := uniqueStrings(filterMissing(in))
		fmt.Fprintf(&b, "_Note: pricing entry missing for %s; values may be undercounted. Update pricing.json._\n\n", strings.Join(missing, ", "))
	}

	maxMonthly := in.AfterCost.Monthly
	if in.BeforeCost.Monthly > maxMonthly {
		maxMonthly = in.BeforeCost.Monthly
	}
	if maxMonthly == 0 {
		maxMonthly = 1
	}
	fmt.Fprintf(&b, "Monthly spend trend:\n")
	fmt.Fprintf(&b, "Before |%s $%.2f\n", bar(in.BeforeCost.Monthly, maxMonthly), in.BeforeCost.Monthly)
	fmt.Fprintf(&b, "After  |%s $%.2f\n\n", bar(in.AfterCost.Monthly, maxMonthly), in.AfterCost.Monthly)

	if len(in.Signals.BeforeModels)+len(in.Signals.AfterModels)+len(in.Signals.BeforeMax)+len(in.Signals.AfterMax)+len(in.Signals.BeforeRetry)+len(in.Signals.AfterRetry) == 0 {
		fmt.Fprintf(&b, "No LLM-cost-relevant changes detected. Current model: %s.\n", in.AfterModel)
		return b.String()
	}

	fmt.Fprintf(&b, "Observed changes (diff-based heuristics):\n")
	if len(in.Signals.BeforeModels) > 0 || len(in.Signals.AfterModels) > 0 {
		fmt.Fprintf(&b, "- Models: %s -> %s\n", listOrPlaceholder(in.Signals.BeforeModels), listOrPlaceholder(in.Signals.AfterModels))
	}
	if len(in.Signals.BeforeMax) > 0 || len(in.Signals.AfterMax) > 0 {
		fmt.Fprintf(&b, "- max_tokens: %s -> %s\n", intsOrDash(in.Signals.BeforeMax), intsOrDash(in.Signals.AfterMax))
	}
	if len(in.Signals.BeforeRetry) > 0 || len(in.Signals.AfterRetry) > 0 {
		fmt.Fprintf(&b, "- retries: %s -> %s\n", intsOrDash(in.Signals.BeforeRetry), intsOrDash(in.Signals.AfterRetry))
	}

	return b.String()
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
	return strings.Repeat("=", filled) + strings.Repeat(".", width-filled)
}

func listOrPlaceholder(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(uniqueStrings(values), ", ")
}

func intsOrDash(values []int) string {
	if len(values) == 0 {
		return "-"
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

func filterMissing(in reportInput) []string {
	var out []string
	if !in.PricingHits.BeforeFound {
		out = append(out, in.BeforeModel)
	}
	if !in.PricingHits.AfterFound {
		out = append(out, in.AfterModel)
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
