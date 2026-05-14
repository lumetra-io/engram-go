# engram-go

Official Go client for [Engram](https://lumetra.io) — durable, explainable memory for AI agents.

- Zero runtime dependencies (uses the standard library's `net/http`).
- `context.Context` on every method; safe for concurrent use.
- Go 1.21+.

Sibling SDKs: [`@lumetra/engram` (TypeScript)](https://github.com/lumetra-io/engram-js), [`lumetra-engram` (Python)](https://github.com/lumetra-io/engram-py).

## Install

```bash
go get github.com/lumetra-io/engram-go@latest
```

## Quickstart

```go
package main

import (
    "context"
    "fmt"

    engram "github.com/lumetra-io/engram-go"
)

func main() {
    client, err := engram.NewClient(engram.Options{
        APIKey: "eng_live_...", // or set ENGRAM_API_KEY and omit
    })
    if err != nil {
        panic(err)
    }

    ctx := context.Background()

    // Store a fact
    _, err = client.StoreMemory(ctx, "User prefers dark mode.", "user-123")
    if err != nil {
        panic(err)
    }

    // Recall — returns a synthesized answer plus the memories that contributed
    res, err := client.Query(ctx, "What are this user's UI preferences?",
        engram.QueryOptions{Buckets: []string{"user-123"}})
    if err != nil {
        panic(err)
    }
    fmt.Println(res.Answer)
    if res.Explanation != nil {
        for _, m := range res.Explanation.RetrievedMemories {
            fmt.Println("-", m.Content)
        }
    }
}
```

## Configuration

```go
engram.NewClient(engram.Options{
    APIKey:     "eng_live_...",       // or ENGRAM_API_KEY env var
    BaseURL:    "https://api.lumetra.io", // or ENGRAM_BASE_URL env var
    Timeout:    30 * time.Second,     // default 30s (ignored if HTTPClient is set)
    HTTPClient: customClient,         // optional, for proxy / retry middleware
    UserAgent:  "myapp/1.0",          // optional
})
```

> **BYOK reminder.** Engram is bring-your-own-key end-to-end. Configure an OpenAI / Anthropic / Groq / Together / Fireworks key on the [Lumetra portal](https://lumetra.io/models) before your first call, or `StoreMemory` / `Query` will return `*engram.Error` with `Status == 412`.

## API surface

All methods take `ctx context.Context` as the first argument.

For every method below, passing `bucket == ""` is shorthand for `"default"` — except for `ClearMemories` and `DeleteBucket`, which **require an explicit non-empty bucket** to prevent accidental data loss.

### Memories
- `StoreMemory(ctx, content, bucket)` — store a single fact (`bucket == ""` ⇒ `"default"`)
- `StoreMemories(ctx, contents, bucket)` — batched store (`bucket == ""` ⇒ `"default"`)
- `ListMemories(ctx, bucket, opts)` — paginated list. `opts` is `ListMemoriesOptions{Limit, Offset}` — `Limit` defaults to 20, `Offset` to 0.
- `DeleteMemory(ctx, memoryID, bucket)` — delete one memory (`bucket == ""` ⇒ `"default"`)
- `ClearMemories(ctx, bucket)` — delete every memory in a bucket. **Empty bucket is rejected.**

### Query
- `Query(ctx, question, opts)` where `opts` is `QueryOptions{Buckets, TopK, SkipSynthesis, ReturnExplanation}`
  - `Buckets` fuses across multiple buckets in one call. Defaults to `[]string{"default"}`.
  - `TopK` defaults to 8.
  - `SkipSynthesis: true` returns retrieval-only — `Answer` will be empty. Defaults to `false`.
  - `ReturnExplanation` defaults to `true`.
  - response shape: `{Answer, Explanation: {RetrievedMemories, Profile, GraphFacts}, Usage}`

### Buckets
- `ListBuckets(ctx)` — all buckets in your tenant
- `CreateBucket(ctx, name, description)` — `description` may be `""`
- `DeleteBucket(ctx, bucket)` — **Empty bucket is rejected.**

### Profile
- `GetProfile(ctx, bucket)` — the canonical profile prepended to recall (`bucket == ""` ⇒ `"default"`)
- `RegenerateProfile(ctx, bucket)` — rebuild from current memories (synchronous, can take seconds; `bucket == ""` ⇒ `"default"`)

## Errors

Every non-2xx response returns `*engram.Error`. Use `errors.As`:

```go
import (
    "errors"
    engram "github.com/lumetra-io/engram-go"
)

_, err := client.StoreMemory(ctx, "...", "user-123")
if err != nil {
    var apiErr *engram.Error
    if errors.As(err, &apiErr) {
        switch apiErr.Status {
        case 412:
            // BYOK not configured — direct user to set a provider key
        case 429:
            // rate limited
        default:
            // other API error
        }
    }
    // else: transport-level error (timeout, DNS, etc.)
}
```

`apiErr.Status` is the HTTP status, `apiErr.Body` is the parsed JSON body (or the raw text fallback if non-JSON).

## Concurrency

`*Client` is safe for concurrent use by multiple goroutines — it holds an `*http.Client`, which is itself goroutine-safe.

## Custom transport

Pass your own `*http.Client` to plug in retries, request signing, telemetry, or a proxy:

```go
httpc := &http.Client{
    Transport: &retryRoundTripper{Base: http.DefaultTransport, Max: 3},
    Timeout:   45 * time.Second,
}
client, _ := engram.NewClient(engram.Options{
    APIKey:     os.Getenv("ENGRAM_API_KEY"),
    HTTPClient: httpc,
})
```

## License

MIT — see [LICENSE](./LICENSE).
