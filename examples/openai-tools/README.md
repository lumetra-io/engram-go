# Engram + OpenAI tools (Go)

Minimal terminal chat loop showing how to expose Engram as OpenAI tool calls in Go. The model decides when to `store_memory` and `query_memory`; this program just wires the tools to `engram.Client` methods.

## Run

```bash
go run .
```

With these env vars set:

```bash
export ENGRAM_API_KEY=eng_live_...
export OPENAI_API_KEY=sk-...
# optional:
export ENGRAM_BUCKET=openai-tools-demo
export OPENAI_MODEL=gpt-4o-mini
```

Type messages. The model has access to two tools:

- `store_memory(content, bucket?)` — save a fact
- `query_memory(question, bucket?)` — recall facts with a synthesized answer

## BYOK reminder

Engram is bring-your-own-key end-to-end. Configure your LLM provider key on the [Lumetra portal](https://lumetra.io/models) before the first call — otherwise `store_memory` / `query_memory` return HTTP 412 and you'll see an `*engram.Error` here.

## Files

- `main.go` — the whole thing, single file. No external deps beyond the Go stdlib and the Engram client (OpenAI Chat Completions is called directly over HTTP, no SDK).
