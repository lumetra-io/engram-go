// Terminal chat loop with Engram memory as OpenAI tool calls.
//
// Run:
//
//	export ENGRAM_API_KEY=eng_live_...
//	export OPENAI_API_KEY=sk-...
//	go run .
//
// The model decides when to store and recall. We just expose the tools.
// Calls OpenAI Chat Completions directly over net/http — no SDK dependency.
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
	"strings"
	"time"

	engram "github.com/lumetra-io/engram-go"
)

const systemPrompt = `You have Engram memory. Use it proactively to improve continuity.

Policy:
- Before answering anything that may rely on prior context, call query_memory.
- Capture stable preferences, profile facts, and decisions via store_memory.
- Keep stored facts atomic and declarative: one concept per memory.`

// ---------- OpenAI types (subset of the API we need) ----------

type oaiMessage struct {
	Role       string          `json:"role"`
	Content    string          `json:"content,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []oaiToolCall   `json:"tool_calls,omitempty"`
}

type oaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaiTool struct {
	Type     string      `json:"type"`
	Function oaiFunction `json:"function"`
}

type oaiFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type oaiRequest struct {
	Model    string       `json:"model"`
	Messages []oaiMessage `json:"messages"`
	Tools    []oaiTool    `json:"tools"`
}

type oaiResponse struct {
	Choices []struct {
		Message oaiMessage `json:"message"`
	} `json:"choices"`
}

// ---------- Tool definitions exposed to the model ----------

func toolDefs(bucket string) []oaiTool {
	return []oaiTool{
		{
			Type: "function",
			Function: oaiFunction{
				Name:        "store_memory",
				Description: "Save a fact to Engram memory.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"content": map[string]interface{}{"type": "string", "description": "The atomic fact to remember."},
						"bucket":  map[string]interface{}{"type": "string", "description": fmt.Sprintf("Bucket name (default: %q).", bucket)},
					},
					"required": []string{"content"},
				},
			},
		},
		{
			Type: "function",
			Function: oaiFunction{
				Name:        "query_memory",
				Description: "Search Engram memory using natural language and get a synthesized answer.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"question": map[string]interface{}{"type": "string", "description": "What to look up."},
						"bucket":   map[string]interface{}{"type": "string", "description": fmt.Sprintf("Bucket name (default: %q).", bucket)},
					},
					"required": []string{"question"},
				},
			},
		},
	}
}

// ---------- Tool dispatch ----------

func dispatchTool(ctx context.Context, e *engram.Client, defaultBucket, name, argsJSON string) (string, error) {
	var args struct {
		Content  string `json:"content"`
		Question string `json:"question"`
		Bucket   string `json:"bucket"`
	}
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("bad tool args: %w", err)
		}
	}
	bucket := args.Bucket
	if bucket == "" {
		bucket = defaultBucket
	}

	switch name {
	case "store_memory":
		res, err := e.StoreMemory(ctx, args.Content, bucket)
		return encodeToolResult(res, err)
	case "query_memory":
		res, err := e.Query(ctx, args.Question, engram.QueryOptions{Buckets: []string{bucket}})
		return encodeToolResult(res, err)
	default:
		return encodeToolResult(nil, fmt.Errorf("unknown tool %q", name))
	}
}

func encodeToolResult(v interface{}, err error) (string, error) {
	if err != nil {
		var eerr *engram.Error
		if errors.As(err, &eerr) {
			b, _ := json.Marshal(map[string]interface{}{"error": eerr.Error(), "status": eerr.Status, "body": eerr.Body})
			return string(b), nil
		}
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(b), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ---------- OpenAI HTTP call ----------

func callOpenAI(ctx context.Context, apiKey, model string, messages []oaiMessage, tools []oaiTool) (oaiMessage, error) {
	body, err := json.Marshal(oaiRequest{Model: model, Messages: messages, Tools: tools})
	if err != nil {
		return oaiMessage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return oaiMessage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return oaiMessage{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return oaiMessage{}, fmt.Errorf("openai %d: %s", resp.StatusCode, raw)
	}
	var out oaiResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return oaiMessage{}, err
	}
	if len(out.Choices) == 0 {
		return oaiMessage{}, fmt.Errorf("openai: empty choices")
	}
	return out.Choices[0].Message, nil
}

// ---------- main loop ----------

func main() {
	if os.Getenv("ENGRAM_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "Set ENGRAM_API_KEY first.")
		os.Exit(1)
	}
	openaiKey := os.Getenv("OPENAI_API_KEY")
	if openaiKey == "" {
		fmt.Fprintln(os.Stderr, "Set OPENAI_API_KEY first.")
		os.Exit(1)
	}

	bucket := os.Getenv("ENGRAM_BUCKET")
	if bucket == "" {
		bucket = "openai-tools-demo"
	}
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "gpt-4o-mini"
	}

	ctx := context.Background()
	e, err := engram.NewClient(engram.Options{}) // picks up ENGRAM_API_KEY
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	tools := toolDefs(bucket)
	messages := []oaiMessage{{Role: "system", Content: systemPrompt}}

	fmt.Printf("Engram bucket: %s  ·  model: %s  ·  Ctrl-D to quit\n\n", bucket, model)

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	for {
		fmt.Print("you> ")
		if !in.Scan() {
			fmt.Println()
			return
		}
		user := strings.TrimSpace(in.Text())
		if user == "" {
			continue
		}
		messages = append(messages, oaiMessage{Role: "user", Content: user})

		// Tool-call loop: keep round-tripping until the model returns a
		// plain assistant message.
		for {
			msg, err := callOpenAI(ctx, openaiKey, model, messages, tools)
			if err != nil {
				fmt.Fprintln(os.Stderr, "openai:", err)
				break
			}
			messages = append(messages, msg)

			if len(msg.ToolCalls) == 0 {
				fmt.Printf("\nagent> %s\n\n", msg.Content)
				break
			}

			for _, call := range msg.ToolCalls {
				out, _ := dispatchTool(ctx, e, bucket, call.Function.Name, call.Function.Arguments)
				messages = append(messages, oaiMessage{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    out,
				})
			}
		}
	}
}
