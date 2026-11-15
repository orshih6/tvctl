// Package claude runs Claude Code headless (`claude -p`) for one request at a
// time: no session, only the tools named (none by default), a JSON Schema for
// the answer. Copied from upworkctl, plus Tools.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// LLM is what the rest of upworkctl needs: a structured answer.
type LLM interface {
	JSON(ctx context.Context, prompt, schema string, out any) error
}

type Runner struct {
	Bin   string // default "claude"
	Model string // e.g. "sonnet"
	// Tools are built-in tools Claude may use (e.g. "WebSearch"); none if empty.
	Tools   []string
	Timeout time.Duration
}

// ErrAuth means the Claude credential was rejected; nothing works until a
// person replaces it.
var ErrAuth = errors.New("claude credential rejected")

// LimitError means the subscription's usage limit is reached.
type LimitError struct{ Msg string }

func (e *LimitError) Error() string { return "claude usage limit: " + e.Msg }

func (r *Runner) JSON(ctx context.Context, prompt, schema string, out any) error {
	bin := r.Bin
	if bin == "" {
		bin = "claude"
	}
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 4 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"-p", "--output-format", "json", "--no-session-persistence",
		"--setting-sources", "", "--strict-mcp-config", "--tools", strings.Join(r.Tools, ",")}
	if len(r.Tools) > 0 {
		args = append(args, "--allowedTools", strings.Join(r.Tools, ","))
	}
	if r.Model != "" {
		args = append(args, "--model", r.Model)
	}
	if schema != "" {
		args = append(args, "--json-schema", schema)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	// The prompt goes over stdin so it can't be read as a flag and stays out of ps.
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("claude timed out after %s", timeout)
	}
	raw, err := parse(stdout.Bytes())
	if err != nil {
		if runErr != nil {
			return fmt.Errorf("claude: %w (%v: %s)", err, runErr, tail(stderr.String(), 300))
		}
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("claude answer does not fit: %w: %s", err, tail(string(raw), 300))
	}
	return nil
}

func parse(stdout []byte) (json.RawMessage, error) {
	var res struct {
		IsError          bool            `json:"is_error"`
		Subtype          string          `json:"subtype"`
		Result           string          `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
	}
	s := strings.TrimSpace(string(stdout))
	if i := strings.LastIndex(s, "\n{"); i >= 0 {
		s = s[i+1:]
	}
	if err := json.Unmarshal([]byte(s), &res); err != nil {
		return nil, fmt.Errorf("unreadable claude output: %s", tail(s, 300))
	}
	if res.IsError {
		for _, m := range []string{"Not logged in", "Failed to authenticate", "Invalid bearer token", "invalid x-api-key", "OAuth token has expired"} {
			if strings.Contains(res.Result, m) {
				return nil, fmt.Errorf("%w: %s", ErrAuth, tail(res.Result, 200))
			}
		}
		if limitRe.MatchString(res.Result) {
			return nil, &LimitError{Msg: tail(res.Result, 200)}
		}
		return nil, fmt.Errorf("claude %s: %s", res.Subtype, tail(res.Result, 300))
	}
	if len(res.StructuredOutput) > 0 && string(res.StructuredOutput) != "null" {
		return res.StructuredOutput, nil
	}
	if obj := lastJSONObject(res.Result); obj != "" {
		return json.RawMessage(obj), nil
	}
	return nil, errors.New("claude gave no structured answer")
}

var limitRe = regexp.MustCompile(`(?i)hit your (session |usage |weekly |daily )?limit|usage limit reached`)

func lastJSONObject(s string) string {
	end := strings.LastIndex(s, "}")
	for start := strings.LastIndex(s[:max(end, 0)], "{"); start >= 0 && end > start; start = strings.LastIndex(s[:start], "{") {
		if c := s[start : end+1]; json.Valid([]byte(c)) {
			return c
		}
	}
	return ""
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
