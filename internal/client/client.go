// Package client shares the authenticated service boundary between project setup
// and native installer wrappers.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	http        *http.Client
	base, token string
}

func New(base, token string, transport *http.Client) Client {
	if transport == nil {
		transport = &http.Client{Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return Client{http: transport, base: strings.TrimRight(base, "/"), token: token}
}

func (c Client) Call(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("service request failed: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil {
		return fmt.Errorf("read service response: %w", err)
	}
	if len(data) > 8<<20 {
		return fmt.Errorf("service response exceeds size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var result struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &result)
		if len(result.Error) > 500 {
			result.Error = ""
		}
		result.Error = strings.Map(func(r rune) rune {
			if r < 32 {
				return ' '
			}
			return r
		}, result.Error)
		if result.Error != "" {
			return fmt.Errorf("service HTTP %d: %s", response.StatusCode, result.Error)
		}
		return fmt.Errorf("service %s %s returned HTTP %d", method, path, response.StatusCode)
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("invalid service JSON: %w", err)
	}
	return nil
}
