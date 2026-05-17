// Package engram is the official Go client for Engram — durable, explainable
// memory for AI agents. See https://lumetra.io for an account.
//
// Quickstart:
//
//	import (
//	    "context"
//	    "fmt"
//	    "github.com/lumetra-io/engram-go"
//	)
//
//	func main() {
//	    client, err := engram.NewClient(engram.Options{APIKey: "eng_live_..."})
//	    if err != nil { panic(err) }
//
//	    ctx := context.Background()
//	    _, _ = client.StoreMemory(ctx, "User prefers dark mode.", "user-123")
//
//	    res, err := client.Query(ctx, "What are this user's preferences?",
//	        engram.QueryOptions{Buckets: []string{"user-123"}})
//	    if err != nil { panic(err) }
//	    fmt.Println(res.Answer)
//	}
package engram

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Version is the released semver of this client.
const Version = "0.5.1"

// DefaultBaseURL is the production Engram API endpoint.
const DefaultBaseURL = "https://api.lumetra.io"

// DefaultTimeout is the default per-request timeout for buffered calls.
const DefaultTimeout = 30 * time.Second

// DefaultStreamTimeout is the default total timeout for QueryStream
// calls. Streaming responses can sit in the prep phase (retrieval +
// extractor pass + count canonicalization) for 5–15s before the first
// synthesis token arrives, so the buffered 30s default would leave no
// headroom for the streamed body. Callers can override via
// Options.StreamTimeout.
const DefaultStreamTimeout = 5 * time.Minute

// DefaultMaxRetriesOn429 is the default retry budget when the server
// returns a 429 (per-tenant concurrent-request cap). Callers can
// override via Options.MaxRetriesOn429. Set to 0 to disable retry.
const DefaultMaxRetriesOn429 = 3

// retryAfterCap bounds the per-attempt sleep duration so a misconfigured
// server can't force callers to wait minutes between retries.
const retryAfterCap = 30 * time.Second

// Options configures a Client.
type Options struct {
	// APIKey looks like "eng_live_...". If empty, falls back to ENGRAM_API_KEY.
	APIKey string
	// BaseURL overrides the API base. Defaults to ENGRAM_BASE_URL or
	// DefaultBaseURL.
	BaseURL string
	// HTTPClient lets you supply a custom *http.Client (for proxies, retry
	// middleware, custom transports). If nil, a client with DefaultTimeout is
	// constructed.
	HTTPClient *http.Client
	// Timeout sets the per-request timeout for buffered (non-streaming)
	// calls if HTTPClient is not provided. Defaults to DefaultTimeout.
	Timeout time.Duration
	// StreamTimeout is the total timeout applied to QueryStream calls.
	// Streaming responses can sit in retrieval + extractor passes for
	// 5–15s before the first token, so the buffered 30s default would
	// fire before the body finishes. Defaults to DefaultStreamTimeout.
	StreamTimeout time.Duration
	// UserAgent overrides the default User-Agent header.
	UserAgent string
	// MaxRetriesOn429 is the retry budget for HTTP 429 responses (the
	// per-tenant concurrent-request cap). The client honors the server's
	// Retry-After header on each retry, capped at 30s per attempt.
	// Zero falls back to DefaultMaxRetriesOn429. Use a negative value
	// to disable retry and surface 429 on the first attempt.
	MaxRetriesOn429 int
}

// Client is a synchronous Engram API client. Methods are safe for concurrent
// use by multiple goroutines.
type Client struct {
	apiKey          string
	baseURL         string
	http            *http.Client
	streamHTTP      *http.Client
	userAgent       string
	maxRetriesOn429 int
}

// NewClient builds a Client from Options. Returns an error if no API key is
// available (neither in opts nor in ENGRAM_API_KEY).
func NewClient(opts Options) (*Client, error) {
	apiKey := opts.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("ENGRAM_API_KEY")
	}
	if apiKey == "" {
		return nil, errors.New("engram: APIKey is required (pass it in Options or set ENGRAM_API_KEY)")
	}

	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = os.Getenv("ENGRAM_BASE_URL")
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	httpClient := opts.HTTPClient
	if httpClient == nil {
		timeout := opts.Timeout
		if timeout == 0 {
			timeout = DefaultTimeout
		}
		httpClient = &http.Client{Timeout: timeout}
	}

	// Streaming uses a separate client so the buffered Timeout doesn't
	// cut long synthesis bodies short. If the caller supplied their own
	// HTTPClient we honor it for streaming too — they took ownership of
	// the timeout knob.
	streamHTTPClient := opts.HTTPClient
	if streamHTTPClient == nil {
		streamTimeout := opts.StreamTimeout
		if streamTimeout == 0 {
			streamTimeout = DefaultStreamTimeout
		}
		streamHTTPClient = &http.Client{Timeout: streamTimeout}
	}

	ua := opts.UserAgent
	if ua == "" {
		ua = "engram-go/" + Version
	}

	maxRetries := opts.MaxRetriesOn429
	switch {
	case maxRetries < 0:
		maxRetries = 0
	case maxRetries == 0:
		maxRetries = DefaultMaxRetriesOn429
	}

	return &Client{
		apiKey:          apiKey,
		baseURL:         baseURL,
		http:            httpClient,
		streamHTTP:      streamHTTPClient,
		userAgent:       ua,
		maxRetriesOn429: maxRetries,
	}, nil
}

// parseRetryAfter resolves a Retry-After header (seconds form) into a
// sleep duration, capped at retryAfterCap. Falls back to the supplied
// default when the header is missing or malformed.
func parseRetryAfter(header string, defaultBackoff time.Duration) time.Duration {
	if header != "" {
		if seconds, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && seconds >= 0 {
			d := time.Duration(seconds) * time.Second
			if d > retryAfterCap {
				return retryAfterCap
			}
			return d
		}
	}
	if defaultBackoff > retryAfterCap {
		return retryAfterCap
	}
	return defaultBackoff
}

// ---------- transport ----------

func (c *Client) request(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u = u + "?" + query.Encode()
	}

	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("engram: marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	// We need to be able to re-issue the request on 429, so buffer
	// the body bytes once and rebuild a fresh *http.Request per attempt
	// (http.Request bodies aren't safely re-readable after Do).
	var bodyBytes []byte
	if reqBody != nil {
		buf, err := io.ReadAll(reqBody)
		if err != nil {
			return fmt.Errorf("engram: read request body: %w", err)
		}
		bodyBytes = buf
	}

	attemptsRemaining := c.maxRetriesOn429
	backoff := time.Second

	for {
		var attemptBody io.Reader
		if bodyBytes != nil {
			attemptBody = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, attemptBody)
		if err != nil {
			return fmt.Errorf("engram: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.userAgent)
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("engram: request: %w", err)
		}

		if resp.StatusCode == 429 && attemptsRemaining > 0 {
			retryAfter := resp.Header.Get("Retry-After")
			// Drain so net/http can reuse the connection on the retry.
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			delay := parseRetryAfter(retryAfter, backoff)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
			attemptsRemaining--
			if backoff *= 2; backoff > retryAfterCap {
				backoff = retryAfterCap
			}
			continue
		}

		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("engram: read response: %w", err)
		}

		var parsed any
		if len(raw) > 0 {
			if jerr := json.Unmarshal(raw, &parsed); jerr != nil {
				parsed = string(raw)
			}
		}

		if resp.StatusCode >= 400 {
			return newError(resp.StatusCode, parsed)
		}

		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("engram: decode response: %w", err)
			}
		}
		return nil
	}
}

// ---------- memories ----------

// StoreMemoryOptions controls per-write store behavior. Only Dedup is
// surfaced today; the struct exists so future options can be added
// without breaking the StoreMemory call site.
type StoreMemoryOptions struct {
	// Dedup controls server-side deduplication for this write. The
	// zero value falls back to the server's default ("loose"). See
	// the DedupOff / DedupLoose / DedupStrict constants and the
	// DedupPolicy doc for semantics.
	Dedup DedupPolicy
}

// StoreMemory stores a single fact in bucket (defaults to "default" if empty).
// Equivalent to StoreMemoryWithOptions with zero-value StoreMemoryOptions.
func (c *Client) StoreMemory(ctx context.Context, content, bucket string) (*StoreMemoryResult, error) {
	return c.StoreMemoryWithOptions(ctx, content, bucket, StoreMemoryOptions{})
}

// StoreMemoryWithOptions is StoreMemory with per-write options. The
// returned StoreMemoryResult includes a Status field — "merged" indicates
// the write was collapsed into an existing memory; check DedupedInto /
// SimilarityScore / MergeReason for details. Customers ingesting
// templated time-series data should pass Dedup: DedupOff to keep
// structurally similar rows from collapsing silently.
func (c *Client) StoreMemoryWithOptions(
	ctx context.Context, content, bucket string, opts StoreMemoryOptions,
) (*StoreMemoryResult, error) {
	bucket = defaultBucket(bucket)
	body := map[string]any{"content": content}
	if opts.Dedup != "" {
		body["dedup"] = string(opts.Dedup)
	}
	var out StoreMemoryResult
	err := c.request(ctx, http.MethodPost,
		"/v1/buckets/"+url.PathEscape(bucket)+"/memories",
		nil,
		body,
		&out,
	)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// StoreMemories stores multiple facts in one batched call.
func (c *Client) StoreMemories(ctx context.Context, contents []string, bucket string) (*StoreMemoriesResult, error) {
	bucket = defaultBucket(bucket)
	items := make([]map[string]string, len(contents))
	for i, content := range contents {
		items[i] = map[string]string{"content": content}
	}
	var out StoreMemoriesResult
	err := c.request(ctx, http.MethodPost,
		"/v1/buckets/"+url.PathEscape(bucket)+"/memories",
		nil,
		map[string]any{"memories": items},
		&out,
	)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListMemories pages through memories in bucket.
func (c *Client) ListMemories(ctx context.Context, bucket string, opts ListMemoriesOptions) (*ListMemoriesResult, error) {
	bucket = defaultBucket(bucket)
	limit := opts.Limit
	if limit == 0 {
		limit = 20
	}
	q := url.Values{}
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(opts.Offset))

	var out ListMemoriesResult
	err := c.request(ctx, http.MethodGet,
		"/v1/buckets/"+url.PathEscape(bucket)+"/memories",
		q, nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteMemory removes one memory by ID.
func (c *Client) DeleteMemory(ctx context.Context, memoryID, bucket string) error {
	bucket = defaultBucket(bucket)
	return c.request(ctx, http.MethodDelete,
		"/v1/buckets/"+url.PathEscape(bucket)+"/memories/"+url.PathEscape(memoryID),
		nil, nil, nil)
}

// ClearMemories removes every memory in bucket. Destructive.
//
// Returns the count of memories actually deleted under ClearedCount —
// the server reports this in the response body and we now surface it.
// Breaking change vs 0.1.x where this returned only error.
func (c *Client) ClearMemories(ctx context.Context, bucket string) (*ClearMemoriesResult, error) {
	// Require an explicit bucket — defaulting an empty string to "default"
	// the way StoreMemory does is unsafe for a destructive op, and an empty
	// string would otherwise hit /v1/buckets//memories.
	if bucket == "" {
		return nil, fmt.Errorf("engram: ClearMemories requires a non-empty bucket name")
	}
	var out ClearMemoriesResult
	if err := c.request(ctx, http.MethodDelete,
		"/v1/buckets/"+url.PathEscape(bucket)+"/memories",
		nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---------- query ----------

// Query runs hybrid retrieval and (optionally) server-side synthesis.
func (c *Client) Query(ctx context.Context, question string, opts QueryOptions) (*QueryResult, error) {
	buckets := opts.Buckets
	if len(buckets) == 0 {
		buckets = []string{"default"}
	}
	topK := opts.TopK
	if topK == 0 {
		topK = 8
	}
	returnExplanation := true
	if opts.ReturnExplanation != nil {
		returnExplanation = *opts.ReturnExplanation
	}

	body := map[string]any{
		"query":   question,
		"buckets": buckets,
		"options": buildQueryOptionsMap(opts, topK, returnExplanation),
	}

	var out QueryResult
	err := c.request(ctx, http.MethodPost, "/v1/query", nil, body, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// buildQueryOptionsMap collapses the public QueryOptions struct into the
// REST options-map shape. Lives here (instead of inline) because Query
// and QueryStream both need it. Zero-value fields are omitted so the
// server uses its defaults.
func buildQueryOptionsMap(opts QueryOptions, topK int, returnExplanation bool) map[string]any {
	m := map[string]any{
		"top_k":              topK,
		"return_explanation": returnExplanation,
		"skip_synthesis":     opts.SkipSynthesis,
	}
	if opts.MaxTokens > 0 {
		m["max_tokens"] = opts.MaxTokens
	}
	if opts.MinSimilarityThreshold != 0 {
		m["min_similarity_threshold"] = opts.MinSimilarityThreshold
	}
	if opts.MinWeightedScore != 0 {
		m["min_weighted_score"] = opts.MinWeightedScore
	}
	if opts.TopKPerBucketMap != nil {
		m["top_k_per_bucket"] = opts.TopKPerBucketMap
	} else if opts.TopKPerBucketInt > 0 {
		m["top_k_per_bucket"] = opts.TopKPerBucketInt
	}
	if opts.ReturnFormat != "" {
		m["return_format"] = opts.ReturnFormat
	}
	if opts.ResponseSchema != nil {
		m["response_schema"] = opts.ResponseSchema
	}
	return m
}

// QueryStream is the streaming variant of Query. It returns a
// *QueryStreamResult that callers iterate with bufio.Scanner-style
// Next() / Event() / Err() / Close().
//
// Usage:
//
//	stream, err := client.QueryStream(ctx, "...", engram.QueryOptions{
//	    Buckets: []string{"default"},
//	})
//	if err != nil { return err }
//	defer stream.Close()
//	for stream.Next() {
//	    ev := stream.Event()
//	    switch ev.Type {
//	    case "delta":
//	        fmt.Print(ev.Content)
//	    case "done":
//	        fmt.Println("usage:", ev.Usage)
//	    }
//	}
//	return stream.Err()
//
// The initial error covers connection / HTTP-status failures; mid-stream
// errors surface via Err() after Next() returns false.
func (c *Client) QueryStream(ctx context.Context, question string, opts QueryOptions) (*QueryStreamResult, error) {
	buckets := opts.Buckets
	if len(buckets) == 0 {
		buckets = []string{"default"}
	}
	topK := opts.TopK
	if topK == 0 {
		topK = 8
	}
	returnExplanation := true
	if opts.ReturnExplanation != nil {
		returnExplanation = *opts.ReturnExplanation
	}

	body := map[string]any{
		"query":   question,
		"buckets": buckets,
		"stream":  true,
		"options": buildQueryOptionsMap(opts, topK, returnExplanation),
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("engram: marshal request body: %w", err)
	}

	// 429-aware retry at connection-open only. Once the response body
	// starts flowing we can't resume mid-stream safely.
	attemptsRemaining := c.maxRetriesOn429
	backoff := time.Second
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.baseURL+"/v1/query", bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("engram: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("User-Agent", c.userAgent)

		resp, err := c.streamHTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("engram: %w", err)
		}

		if resp.StatusCode == 429 && attemptsRemaining > 0 {
			retryAfter := resp.Header.Get("Retry-After")
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			delay := parseRetryAfter(retryAfter, backoff)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			attemptsRemaining--
			if backoff *= 2; backoff > retryAfterCap {
				backoff = retryAfterCap
			}
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			// Buffer the error body so the caller gets the same Error
			// shape they'd see from the non-streaming Query() path.
			errBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var parsed any
			if len(errBody) > 0 {
				_ = json.Unmarshal(errBody, &parsed)
			}
			detail := errBody
			if m, ok := parsed.(map[string]any); ok {
				if e, ok := m["error"].(string); ok {
					detail = []byte(e)
				}
			}
			return nil, &Error{
				Status:  resp.StatusCode,
				Message: fmt.Sprintf("Engram API %d: %s", resp.StatusCode, string(detail)),
				Body:    parsed,
			}
		}

		return &QueryStreamResult{
			resp:    resp,
			scanner: bufio.NewScanner(resp.Body),
		}, nil
	}
}

// QueryStreamResult is the iterator returned by Client.QueryStream. Use
// Next() to advance, Event() to read the current event, Err() to check
// for mid-stream errors, and Close() to release the underlying
// connection (deferred call recommended).
type QueryStreamResult struct {
	resp    *http.Response
	scanner *bufio.Scanner
	current QueryStreamEvent
	err     error
	done    bool
}

// Next advances to the next event. Returns false when the stream ends
// (either naturally with a [DONE] sentinel, or because of an error —
// check Err() in the latter case).
func (s *QueryStreamResult) Next() bool {
	if s.done {
		return false
	}
	// SSE frames are separated by blank lines. Each frame's data line
	// looks like "data: <json>" (or "data: [DONE]"). We accumulate
	// data: lines until a blank line, then parse.
	var dataBuf strings.Builder
	for s.scanner.Scan() {
		line := s.scanner.Text()
		if line == "" {
			// End of frame. If we collected any data, parse it.
			if dataBuf.Len() == 0 {
				// Empty frame; keep scanning.
				continue
			}
			payload := dataBuf.String()
			dataBuf.Reset()
			if payload == "[DONE]" {
				s.done = true
				return false
			}
			// OpenAI-style chunk: {"choices":[{"delta":{"content":"..."}}]}
			// Or final frame: {"usage":...,"synthesis_usage":...,"explanation":...}
			// Or error frame: {"error":"..."}
			var raw map[string]any
			if jerr := json.Unmarshal([]byte(payload), &raw); jerr != nil {
				// Malformed frame; skip rather than abort the whole stream.
				continue
			}
			if eMsg, ok := raw["error"].(string); ok {
				s.err = &Error{Status: 0, Message: eMsg, Body: raw}
				s.done = true
				return false
			}
			if choices, ok := raw["choices"].([]any); ok && len(choices) > 0 {
				if first, ok := choices[0].(map[string]any); ok {
					if delta, ok := first["delta"].(map[string]any); ok {
						if content, ok := delta["content"].(string); ok && content != "" {
							s.current = QueryStreamEvent{Type: "delta", Content: content}
							return true
						}
					}
				}
				// Empty delta — keep scanning.
				continue
			}
			// Final usage / explanation frame. Re-decode strict to
			// preserve nested structure (QueryUsage / QueryExplanation).
			var final QueryStreamEvent
			if jerr := json.Unmarshal([]byte(payload), &final); jerr == nil {
				final.Type = "done"
				s.current = final
				return true
			}
			// If strict decode fails, fall through with a generic done
			// event carrying whatever fields we could pull out.
			s.current = QueryStreamEvent{Type: "done"}
			return true
		}
		if strings.HasPrefix(line, "data: ") {
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(line[6:])
		} else if strings.HasPrefix(line, "data:") {
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(line[5:])
		}
		// Ignore comment / event: / id: lines — we don't use them.
	}
	// Scanner stopped: either EOF or an error.
	if err := s.scanner.Err(); err != nil {
		s.err = fmt.Errorf("engram: stream read: %w", err)
	}
	// Flush trailing data buffer (servers that don't terminate with a
	// blank line).
	if dataBuf.Len() > 0 {
		payload := dataBuf.String()
		if payload != "[DONE]" {
			var raw map[string]any
			if jerr := json.Unmarshal([]byte(payload), &raw); jerr == nil {
				if choices, ok := raw["choices"].([]any); ok && len(choices) > 0 {
					if first, ok := choices[0].(map[string]any); ok {
						if delta, ok := first["delta"].(map[string]any); ok {
							if content, ok := delta["content"].(string); ok && content != "" {
								s.current = QueryStreamEvent{Type: "delta", Content: content}
								s.done = true
								return true
							}
						}
					}
				}
				var final QueryStreamEvent
				if jerr := json.Unmarshal([]byte(payload), &final); jerr == nil {
					final.Type = "done"
					s.current = final
					s.done = true
					return true
				}
			}
		}
	}
	s.done = true
	return false
}

// Event returns the event produced by the most recent successful Next()
// call. Calling Event() before Next() returns true is undefined.
func (s *QueryStreamResult) Event() QueryStreamEvent {
	return s.current
}

// Err returns the first non-nil error encountered while streaming, if
// any. Call after Next() returns false to distinguish a clean end-of-
// stream from a mid-stream failure.
func (s *QueryStreamResult) Err() error {
	return s.err
}

// Close releases the underlying HTTP connection. Always defer this
// after a successful QueryStream call, even if you iterate to natural
// completion — it returns the connection to the keep-alive pool.
func (s *QueryStreamResult) Close() error {
	if s.resp != nil && s.resp.Body != nil {
		// Drain any unread bytes so net/http can reuse the connection.
		_, _ = io.Copy(io.Discard, s.resp.Body)
		return s.resp.Body.Close()
	}
	return nil
}

// ---------- buckets ----------

// ListBuckets returns every bucket in your tenant.
func (c *Client) ListBuckets(ctx context.Context) ([]Bucket, error) {
	var raw json.RawMessage
	err := c.request(ctx, http.MethodGet, "/v1/buckets", nil, nil, &raw)
	if err != nil {
		return nil, err
	}
	// Server may respond with either {buckets: [...]} or a bare [...] array.
	var arr []Bucket
	if jerr := json.Unmarshal(raw, &arr); jerr == nil {
		return arr, nil
	}
	var wrapped struct {
		Buckets []Bucket `json:"buckets"`
	}
	if jerr := json.Unmarshal(raw, &wrapped); jerr != nil {
		return nil, fmt.Errorf("engram: decode buckets response: %w", jerr)
	}
	return wrapped.Buckets, nil
}

// CreateBucket registers a new bucket. description is optional ("" omits it).
func (c *Client) CreateBucket(ctx context.Context, name, description string) (*Bucket, error) {
	body := map[string]any{"name": name}
	if description != "" {
		body["description"] = description
	}
	var out Bucket
	err := c.request(ctx, http.MethodPost, "/v1/buckets", nil, body, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteBucket removes a bucket and all of its memories.
func (c *Client) DeleteBucket(ctx context.Context, bucket string) error {
	if bucket == "" {
		return fmt.Errorf("engram: DeleteBucket requires a non-empty bucket name")
	}
	return c.request(ctx, http.MethodDelete,
		"/v1/buckets/"+url.PathEscape(bucket),
		nil, nil, nil)
}

// ---------- profile ----------

// GetProfile returns the canonical profile prepended to recall for bucket.
func (c *Client) GetProfile(ctx context.Context, bucket string) (*ProfileResult, error) {
	bucket = defaultBucket(bucket)
	var out ProfileResult
	err := c.request(ctx, http.MethodGet,
		"/v1/buckets/"+url.PathEscape(bucket)+"/profile",
		nil, nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// RegenerateProfile rebuilds the profile for bucket from current memories.
// Synchronous; can take several seconds.
func (c *Client) RegenerateProfile(ctx context.Context, bucket string) (*ProfileResult, error) {
	bucket = defaultBucket(bucket)
	var out ProfileResult
	err := c.request(ctx, http.MethodPost,
		"/v1/buckets/"+url.PathEscape(bucket)+"/profile/regenerate",
		nil, nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func defaultBucket(bucket string) string {
	if bucket == "" {
		return "default"
	}
	return bucket
}
