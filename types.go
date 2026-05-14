package engram

// Bucket is a memory namespace.
type Bucket struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
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
	ID         string `json:"id"`
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

// QueryExplanation explains where a query answer came from.
type QueryExplanation struct {
	RetrievedMemories []RetrievedMemory `json:"retrieved_memories,omitempty"`
	Profile           *string           `json:"profile,omitempty"`
	GraphFacts        []string          `json:"graph_facts,omitempty"`
}

// QueryUsage reports LLM token usage for the server-side synthesis call.
type QueryUsage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

// QueryResult is the response from Query.
type QueryResult struct {
	Answer      string            `json:"answer"`
	Explanation *QueryExplanation `json:"explanation,omitempty"`
	Usage       *QueryUsage       `json:"usage,omitempty"`
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

// ProfileResult is the response from GetProfile / RegenerateProfile.
type ProfileResult struct {
	Profile *string `json:"profile"`
}

// BoolPtr is a tiny helper for setting *bool fields in option structs.
func BoolPtr(b bool) *bool { return &b }
