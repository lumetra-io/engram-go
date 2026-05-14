package engram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testServer spins up an httptest server and returns a client pointed at it,
// plus a slice that captures each request the test handler received.
func testServer(t *testing.T, handler http.HandlerFunc) (*Client, *[]*http.Request) {
	t.Helper()
	captured := make([]*http.Request, 0)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Buffer the body so the test handler can read it again.
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		// Stash a copy for assertions.
		copyReq := r.Clone(r.Context())
		copyReq.Body = io.NopCloser(strings.NewReader(string(body)))
		captured = append(captured, copyReq)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(Options{APIKey: "test-key", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c, &captured
}

func TestNewClient_RequiresAPIKey(t *testing.T) {
	t.Setenv("ENGRAM_API_KEY", "")
	if _, err := NewClient(Options{}); err == nil {
		t.Fatal("expected error when APIKey missing")
	}
}

func TestNewClient_FromEnv(t *testing.T) {
	t.Setenv("ENGRAM_API_KEY", "env-key")
	t.Setenv("ENGRAM_BASE_URL", "https://example.test")
	c, err := NewClient(Options{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.apiKey != "env-key" {
		t.Errorf("apiKey = %q, want env-key", c.apiKey)
	}
	if c.baseURL != "https://example.test" {
		t.Errorf("baseURL = %q, want example.test", c.baseURL)
	}
}

func TestNewClient_TrimTrailingSlash(t *testing.T) {
	c, err := NewClient(Options{APIKey: "k", BaseURL: "https://api.example.com////"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.baseURL != "https://api.example.com" {
		t.Errorf("baseURL = %q, want trimmed", c.baseURL)
	}
}

func TestStoreMemory_PostsToBucketPath(t *testing.T) {
	c, captured := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"mem_1","bucket_name":"user-42","token_count":12}`))
	})

	res, err := c.StoreMemory(context.Background(), "User likes red.", "user-42")
	if err != nil {
		t.Fatalf("StoreMemory: %v", err)
	}
	if res.ID != "mem_1" || res.BucketName != "user-42" || res.TokenCount != 12 {
		t.Errorf("unexpected result: %+v", res)
	}

	req := (*captured)[0]
	if req.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", req.Method)
	}
	if req.URL.Path != "/v1/buckets/user-42/memories" {
		t.Errorf("path = %s", req.URL.Path)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("Authorization = %q", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}

	body, _ := io.ReadAll(req.Body)
	var decoded map[string]string
	_ = json.Unmarshal(body, &decoded)
	if decoded["content"] != "User likes red." {
		t.Errorf("body = %s", body)
	}
}

func TestStoreMemory_DefaultBucket(t *testing.T) {
	c, captured := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"x","bucket_name":"default","token_count":1}`))
	})
	_, err := c.StoreMemory(context.Background(), "x", "")
	if err != nil {
		t.Fatal(err)
	}
	if (*captured)[0].URL.Path != "/v1/buckets/default/memories" {
		t.Errorf("path = %s", (*captured)[0].URL.Path)
	}
}

func TestStoreMemories_BatchShape(t *testing.T) {
	c, captured := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"memories":[{"id":"a","bucket_name":"b","token_count":1},{"id":"b","bucket_name":"b","token_count":2}]}`))
	})
	res, err := c.StoreMemories(context.Background(), []string{"one", "two"}, "b")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Memories) != 2 {
		t.Fatalf("len = %d", len(res.Memories))
	}

	body, _ := io.ReadAll((*captured)[0].Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	mems, _ := decoded["memories"].([]any)
	if len(mems) != 2 {
		t.Errorf("batch len = %d", len(mems))
	}
}

func TestListMemories_QueryParams(t *testing.T) {
	c, captured := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"memories":[],"total":0,"limit":50,"offset":10}`))
	})
	_, err := c.ListMemories(context.Background(), "b", ListMemoriesOptions{Limit: 50, Offset: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := (*captured)[0].URL.Query()
	if got.Get("limit") != "50" || got.Get("offset") != "10" {
		t.Errorf("query = %v", got)
	}
}

func TestListMemories_DefaultLimit(t *testing.T) {
	c, captured := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"memories":[],"total":0,"limit":20,"offset":0}`))
	})
	_, err := c.ListMemories(context.Background(), "b", ListMemoriesOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if (*captured)[0].URL.Query().Get("limit") != "20" {
		t.Errorf("default limit not applied: %s", (*captured)[0].URL.RawQuery)
	}
}

func TestDeleteMemory(t *testing.T) {
	c, captured := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.DeleteMemory(context.Background(), "mem-1", "b"); err != nil {
		t.Fatal(err)
	}
	req := (*captured)[0]
	if req.Method != http.MethodDelete || req.URL.Path != "/v1/buckets/b/memories/mem-1" {
		t.Errorf("req = %s %s", req.Method, req.URL.Path)
	}
}

func TestClearMemories(t *testing.T) {
	c, captured := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.ClearMemories(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	if (*captured)[0].URL.Path != "/v1/buckets/b/memories" {
		t.Errorf("path = %s", (*captured)[0].URL.Path)
	}
}

func TestQuery_BodyShape(t *testing.T) {
	c, captured := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"answer":"red","explanation":{"retrieved_memories":[{"content":"User likes red.","score":0.9}]}}`))
	})

	res, err := c.Query(context.Background(), "color?", QueryOptions{
		Buckets: []string{"a", "b"},
		TopK:    4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != "red" {
		t.Errorf("answer = %q", res.Answer)
	}
	if res.Explanation == nil || len(res.Explanation.RetrievedMemories) != 1 {
		t.Errorf("explanation missing")
	}

	body, _ := io.ReadAll((*captured)[0].Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	if decoded["query"] != "color?" {
		t.Errorf("query field = %v", decoded["query"])
	}
	bs, _ := decoded["buckets"].([]any)
	if len(bs) != 2 {
		t.Errorf("buckets = %v", decoded["buckets"])
	}
	opts, _ := decoded["options"].(map[string]any)
	if opts["top_k"].(float64) != 4 {
		t.Errorf("top_k = %v", opts["top_k"])
	}
	if opts["return_explanation"].(bool) != true {
		t.Errorf("return_explanation defaulted wrong")
	}
}

func TestQuery_DefaultBuckets(t *testing.T) {
	c, captured := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"answer":""}`))
	})
	_, err := c.Query(context.Background(), "?", QueryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll((*captured)[0].Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	bs, _ := decoded["buckets"].([]any)
	if len(bs) != 1 || bs[0] != "default" {
		t.Errorf("default buckets = %v", decoded["buckets"])
	}
}

func TestListBuckets_WrappedResponse(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"buckets":[{"id":"1","name":"a","created_at":"2026-01-01"}]}`))
	})
	bs, err := c.ListBuckets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 1 || bs[0].Name != "a" {
		t.Errorf("buckets = %+v", bs)
	}
}

func TestListBuckets_BareArrayResponse(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"1","name":"a","created_at":"2026-01-01"}]`))
	})
	bs, err := c.ListBuckets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 1 || bs[0].Name != "a" {
		t.Errorf("buckets = %+v", bs)
	}
}

func TestCreateBucket(t *testing.T) {
	c, captured := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"new","name":"team","created_at":"2026-01-01"}`))
	})
	b, err := c.CreateBucket(context.Background(), "team", "shared")
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "team" {
		t.Errorf("name = %s", b.Name)
	}
	body, _ := io.ReadAll((*captured)[0].Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	if decoded["description"] != "shared" {
		t.Errorf("description = %v", decoded["description"])
	}
}

func TestGetAndRegenerateProfile(t *testing.T) {
	c, captured := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"profile":"the canonical profile text"}`))
	})

	got, err := c.GetProfile(context.Background(), "b")
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile == nil || *got.Profile != "the canonical profile text" {
		t.Errorf("get profile = %+v", got)
	}

	_, err = c.RegenerateProfile(context.Background(), "b")
	if err != nil {
		t.Fatal(err)
	}
	if (*captured)[1].Method != http.MethodPost || (*captured)[1].URL.Path != "/v1/buckets/b/profile/regenerate" {
		t.Errorf("regenerate path: %s %s", (*captured)[1].Method, (*captured)[1].URL.Path)
	}
}

func TestErrorOn412IsTyped(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = w.Write([]byte(`{"error":"BYOK not configured"}`))
	})
	_, err := c.StoreMemory(context.Background(), "x", "b")
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *engram.Error, got %T", err)
	}
	if apiErr.Status != 412 {
		t.Errorf("status = %d", apiErr.Status)
	}
	if !strings.Contains(apiErr.Error(), "BYOK") {
		t.Errorf("message = %q", apiErr.Error())
	}
}

func TestErrorOn500NonJSON(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`upstream timeout`))
	})
	_, err := c.StoreMemory(context.Background(), "x", "b")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 500 {
		t.Fatalf("expected 500 *engram.Error, got %v", err)
	}
}

func TestContextCancellation(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.StoreMemory(ctx, "x", "b")
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestUserAgentDefault(t *testing.T) {
	c, captured := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	_ = c.ClearMemories(context.Background(), "b")
	got := (*captured)[0].Header.Get("User-Agent")
	if !strings.HasPrefix(got, "engram-go/") {
		t.Errorf("User-Agent = %q", got)
	}
}

func TestClearMemories_EmptyBucketErrors(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server should not be called when bucket is empty; got %s %s", r.Method, r.URL.Path)
	})
	if err := c.ClearMemories(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty bucket, got nil")
	}
}

func TestDeleteBucket_EmptyBucketErrors(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server should not be called when bucket is empty; got %s %s", r.Method, r.URL.Path)
	})
	if err := c.DeleteBucket(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty bucket, got nil")
	}
}
