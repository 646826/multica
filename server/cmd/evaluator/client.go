package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Client struct {
	base       string
	token      string
	httpClient *http.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{
		base:       strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

type ClaimRequest struct {
	EvaluatorID   string   `json:"evaluator_id"`
	AdapterKinds  []string `json:"adapter_kinds"`
	MaxConcurrent int      `json:"max_concurrent"`
}

type ClaimedJob struct {
	JobID                 string          `json:"job_id"`
	TaskID                string          `json:"task_id"`
	InstanceID            string          `json:"instance_id"`
	InstanceMeta          json.RawMessage `json:"instance_meta"`
	AdapterKind           string          `json:"adapter_kind"`
	AttachmentID          string          `json:"attachment_id,omitempty"`
	SubmissionDownloadURL string          `json:"submission_download_url,omitempty"`
}

func (c *Client) Claim(ctx context.Context, req ClaimRequest) ([]ClaimedJob, error) {
	body, _ := json.Marshal(req)
	out, err := c.do(ctx, http.MethodPost, "/api/internal/eval-jobs/claim", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var jobs []ClaimedJob
	if err := json.Unmarshal(out, &jobs); err != nil {
		return nil, fmt.Errorf("decode claim response: %w (body=%s)", err, string(out))
	}
	return jobs, nil
}

type CompleteRequest struct {
	Resolved         bool            `json:"resolved"`
	PassedTests      int             `json:"passed_tests"`
	TotalTests       int             `json:"total_tests"`
	PassRate         float64         `json:"pass_rate"`
	RawEvalJSON      json.RawMessage `json:"raw_eval_json"`
	FailedCategories []string        `json:"failed_categories"`
}

func (c *Client) Complete(ctx context.Context, jobID string, req CompleteRequest) error {
	body, _ := json.Marshal(req)
	_, err := c.do(ctx, http.MethodPost, "/api/internal/eval-jobs/"+jobID+"/complete", bytes.NewReader(body))
	return err
}

func (c *Client) Fail(ctx context.Context, jobID, lastError string) error {
	body, _ := json.Marshal(map[string]string{"last_error": lastError})
	_, err := c.do(ctx, http.MethodPost, "/api/internal/eval-jobs/"+jobID+"/fail", bytes.NewReader(body))
	return err
}

// DownloadSubmission writes the file at relativeURL to dst.
func (c *Client) DownloadSubmission(ctx context.Context, relativeURL, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+relativeURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("download submission: %s (body=%s)", resp.Status, string(b))
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return out, fmt.Errorf("multica: %s %s -> %s (body=%s)", method, path, resp.Status, string(out))
	}
	return out, nil
}
