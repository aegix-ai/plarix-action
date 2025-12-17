# Pricing Sources

Official pricing pages for LLM providers:

| Provider | URL | Last Verified |
|----------|-----|---------------|
| OpenAI | https://platform.openai.com/docs/pricing | 2025-12-17 |
| Anthropic | https://www.anthropic.com/pricing | 2025-12-17 |

## Notes

- **OpenAI**: The official pricing page at `platform.openai.com/docs/pricing` lists all model prices per 1M tokens.
- **Anthropic**: The official pricing page at `anthropic.com/pricing` lists Claude model prices per 1M tokens.

## Pricing Data Format

All prices in `pricing.json` are stored as **USD per 1 million tokens**:
- `input_per_million`: Cost per 1M input tokens
- `output_per_million`: Cost per 1M output tokens

## Update Process

1. Visit the official pricing pages above
2. Update the prices in `cmd/update-pricing/main.go`
3. Run `make update-pricing`
4. Update the "Last Verified" dates in this file
5. Commit all changes

## Verification Checklist

When updating prices, verify:
- [ ] OpenAI GPT-4o, GPT-4o-mini prices match official page
- [ ] OpenAI o1, o1-mini, o3, o3-mini prices match official page
- [ ] Anthropic Claude Sonnet, Haiku, Opus prices match official page
- [ ] `last_updated` field in pricing.json is updated
- [ ] `sources` array in pricing.json contains correct URLs

