# plarix-action

Minimal GitHub Action that estimates LLM spend impact for Pull Requests (OpenAI + Anthropic) using a bundled pricing table. No external services, no telemetry, no dashboards -- just a tiny Go binary that reads PR diffs.

## 60-second install

1. Copy `.github/workflows/plarix.yml` into your repo (uses `plarix-dev/plarix-action@v0`).
2. Optional: drop a `.plarix.yml` with your usage assumptions (requests per day, token sizes, provider/model).
3. Push -- the action comments on PRs (if `pull-requests: write` permission) and writes to the job summary.

## What you get

- Per-request and monthly spend estimate using the chosen model + assumptions.
- Diff-aware signals: model strings, `max_tokens`, and retry counts pulled from PR patches.
- ASCII monthly spend bars and a compact before/after table.
- Bundled pricing (`pricing.json`) with explicit sources (`PRICING_SOURCES.md`); no runtime network fetch.
- If nothing relevant changed, the report says so and still shows the current estimate.

Example output:

````markdown
<!-- plarix-action -->

### LLM cost check
Pricing: 2024-08-20 (sources: https://openai.com/pricing, https://www.anthropic.com/pricing)

Assumptions: 10000 req/day | 800 in tokens | 400 out tokens | openai/gpt-4o-mini

| | Model | Est. per request | Est. monthly |
|---|---|---|---|
| Before | gpt-4o-mini | $0.0004 | $108.00 |
| After  | gpt-4o      | $0.0100 | $3000.00 |

Monthly spend trend:
Before |=..................... $108.00
After  |====================== $3000.00

Observed changes (diff-based heuristics):
- Models: gpt-4o-mini -> gpt-4o
- max_tokens: - -> 2048
`````

## Configuration (`.plarix.yml`)

If absent, defaults are used and noted in the report.

```yaml
assumptions:
  requests_per_day: 10000
  avg_input_tokens: 800
  avg_output_tokens: 400
  provider: "openai"   # or "anthropic"
  model: "gpt-4o-mini" # or "claude-3-5-sonnet-latest"
```

## Pricing data

- `pricing.json` holds the bundled table and `last_updated` date.
- `PRICING_SOURCES.md` links to the official pricing pages and records the last checked date.
- Update script: `make update-pricing` (runs `go run ./cmd/update-pricing` and rewrites `pricing.json`). Adjust the hardcoded table in `cmd/update-pricing/main.go` if prices change.

The action never fetches pricing over the network at runtime.

## How it works

1. Downloads the published linux-amd64 binary from GitHub Releases (see `action.yml`).
2. Reads GitHub context (`GITHUB_REPOSITORY`, `GITHUB_EVENT_PATH`, `GITHUB_TOKEN`), lists PR files, and scans patches with regexes for model names, `max_tokens`, and retry counts.
3. Computes before/after estimates using the assumptions + bundled pricing, then:
   - Writes to the GitHub job summary.
   - Updates or creates a PR comment marked with `<!-- plarix-action -->` (skips silently if the token lacks permission).

## Security notes

- No outbound calls beyond the GitHub API for the PR diff.
- Pricing is read from the repo; no runtime scraping or third-party services.
- No code execution of PR contents; only text scanning of patches.
- Uses only the Go standard library at runtime.

## Limits and scope

- Heuristics are intentionally simple and may miss complex config changes.
- Unknown models fall back to the closest provider entry and are flagged in the report.
- Only linux-amd64 binaries are published by default; extend the release workflow if you need more.
