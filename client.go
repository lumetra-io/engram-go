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
const Version = "0.2.0"

// DefaultBaseURL is the production Engram API endpoint.
const DefaultBaseURL = "https://api.lumetra.io"

// DefaultTimeout is the default per-request timeout.
const DefaultTimeout = 30 * time.Second

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
	// Timeout sets the per-request timeout if HTTPClient is not provided.
	// Defaults to DefaultTimeout.
	Timeout time.Duration
	// UserAgent overrides the default User-Agent header.
	UserAgent string
}

// Client is a synchronous Engram API client. Methods are safe for concurrent
// use by multiple goroutines.
type Client struct {
	apiKey    string
	baseURL   string
	http      *http.Client
	userAgent string
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

	ua := opts.UserAgent
	if ua == "" {
		ua = "engram-go/" + Version
	}

	return &Client{
		apiKey:    apiKey,
		baseURL:   baseURL,
		http:      httpClient,
		userAgent: ua,
	}, nil
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

	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return fmt.Errorf("engram: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("engram: request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
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

// ---------- memories ----------

// StoreMemory stores a single fact in bucket (defaults to "default" if empty).
func (c *Client) StoreMemory(ctx context.Context, content, bucket string) (*StoreMemoryResult, error) {
	bucket = defaultBucket(bucket)
	var out StoreMemoryResult
	err := c.request(ctx, http.MethodPost,
		"/v1/buckets/"+url.PathEscape(bucket)+"/memories",
		nil,
		map[string]string{"content": content},
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
		"options": map[string]any{
			"top_k":              topK,
			"return_explanation": returnExplanation,
			"skip_synthesis":     opts.SkipSynthesis,
		},
	}

	var out QueryResult
	err := c.request(ctx, http.MethodPost, "/v1/query", nil, body, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
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
