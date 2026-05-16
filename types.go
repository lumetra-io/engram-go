package engram

// Bucket is a memory namespace.
type Bucket struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// BucketName mirrors Name. The server emits both so callers
	// iterating both ListBuckets and StoreMemory responses can use one
	// field name. Prefer Name in new code.
	BucketName  string  `json:"bucket_name,omitempty"`
	Description *string `json:"description,omitempty"`
	CreatedAt   string  `json:"created_at"`
	MemoryCount *int    `json:"memory_count,omitempty"`
}

// Memory is a single stored fact.
type Memory struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	BucketName string `json:"bucket_name,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	TokenCount int    `json:"token_count,omitempty"`
}

// StoreMemoryResult is returned by StoreMemory.
type StoreMemoryResult struct {
	ID string `json:"id"`
	// MemoryID is an alias for ID — older API docs / older SDKs
	// referenced this name. Always present; prefer ID in new code.
	MemoryID   string `json:"memory_id,omitempty"`
	BucketName string `json:"bucket_name"`
	TokenCount int    `json:"token_count"`
}

// StoreMemoriesResult is returned by StoreMemories (batched).
type StoreMemoriesResult struct {
	Memories []StoreMemoryResult `json:"memories"`
}

// RetrievedMemory is one row from the explanation payload.
type RetrievedMemory struct {
	ID      string   `json:"id,omitempty"`
	Content string   `json:"content"`
	Score   *float64 `json:"score,omitempty"`
	Bucket  string   `json:"bucket,omitempty"`
}

// GraphFact is a single (subject, predicate, object) triple surfaced in a
// query explanation. The server emits objects (not strings) — typing this as
// []string previously caused every Query with ReturnExplanation=true to fail
// JSON decoding.
type GraphFact struct {
	Subject    string `json:"subject"`
	Predicate  string `json:"predicate"`
	Object     string `json:"object"`
	BucketName string `json:"bucket_name,omitempty"`
}

// QueryExplanation explains where a query answer came from.
type QueryExplanation struct {
	RetrievedMemories []RetrievedMemory `json:"retrieved_memories,omitempty"`
	Profile           *string           `json:"profile,omitempty"`
	GraphFacts        []GraphFact       `json:"graph_facts,omitempty"`
}

// QueryUsage reports token usage for a query. InputTokens / OutputTokens
// are the customer-facing counts (their question + the model's answer).
// ContextTokens is the size of the retrieved context the server added on
// top of the question. ActualInputTokens / ActualOutputTokens are the
// raw counts the underlying LLM saw — useful for cost reconciliation.
//
// Field-tag change vs 0.2.x: previously the tags read prompt_tokens /
// completion_tokens, which never matched the server response, so
// QueryUsage values silently decoded as zeros. 0.3.0 fixes the tags.
type QueryUsage struct {
	InputTokens        int `json:"input_tokens,omitempty"`
	OutputTokens       int `json:"output_tokens,omitempty"`
	ContextTokens      int `json:"context_tokens,omitempty"`
	ActualInputTokens  int `json:"actual_input_tokens,omitempty"`
	ActualOutputTokens int `json:"actual_output_tokens,omitempty"`
}

// QueryResult is the response from Query.
type QueryResult struct {
	Answer string `json:"answer"`
	// MemoriesFound is the top-level count of retrieved memories.
	// Equivalent to len(Explanation.RetrievedMemories) but present even
	// when ReturnExplanation is false. omitempty so a 0 doesn't shadow
	// a real "no matches" response.
	MemoriesFound int               `json:"memories_found,omitempty"`
	Explanation   *QueryExplanation `json:"explanation,omitempty"`
	Usage         *QueryUsage       `json:"usage,omitempty"`
}

// QueryOptions are tunable parameters for Query.
type QueryOptions struct {
	// Buckets to fuse across. If empty, defaults to []string{"default"}.
	Buckets []string
	// TopK is the maximum number of memories to retrieve. Zero defaults to 8.
	TopK int
	// SkipSynthesis, if true, returns retrieval-only — server skips the LLM call
	// and Answer will be empty.
	SkipSynthesis bool
	// ReturnExplanation populates the Explanation field. Defaults to true (unset
	// is treated as true via the *bool below).
	ReturnExplanation *bool
}

// ListMemoriesOptions are tunable parameters for ListMemories.
type ListMemoriesOptions struct {
	// Limit defaults to 20.
	Limit int
	// Offset defaults to 0.
	Offset int
}

// ListMemoriesResult is the response from ListMemories.
type ListMemoriesResult struct {
	Memories []Memory `json:"memories"`
	Total    int      `json:"total"`
	Limit    int      `json:"limit"`
	Offset   int      `json:"offset"`
}

// ClearMemoriesResult is the response from ClearMemories. cleared_count
// is the number of memories actually deleted (server-reported).
type ClearMemoriesResult struct {
	Success      bool `json:"success"`
	ClearedCount int  `json:"cleared_count"`
}

// ProfileResult is the response from GetProfile / RegenerateProfile.
type ProfileResult struct {
	Profile *string `json:"profile"`
}

// BoolPtr is a tiny helper for setting *bool fields in option structs.
func BoolPtr(b bool) *bool { return &b }

// QueryStreamEvent is one frame yielded by Client.QueryStream. Two
// shapes share this struct, discriminated by Type:
//   - Type == "delta" — Content holds an incremental piece of the answer.
//   - Type == "done"  — Usage / SynthesisUsage / Explanation hold the
//     final synthesis usage and (optionally) retrieval explanation.
//     Emitted exactly once at the end of the stream.
type QueryStreamEvent struct {
	Type            string            `json:"type"`
	Content         string            `json:"content,omitempty"`
	Usage           *QueryUsage       `json:"usage,omitempty"`
	SynthesisUsage  any               `json:"synthesis_usage,omitempty"`
	Explanation     *QueryExplanation `json:"explanation,omitempty"`
}
