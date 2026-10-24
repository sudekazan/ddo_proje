// Package apiclient is a small JSON client for the DişHekimim API, used by the simulator
// and by the end-to-end verification tool.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
	Token   string
	Refresh string
	User    map[string]any
}

func New(baseURL string) *Client {
	return &Client{BaseURL: baseURL, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Response keeps the raw outcome so callers can assert on status codes and error codes.
type Response struct {
	Status  int
	Body    []byte
	Headers http.Header
}

func (r Response) JSON(v any) error { return json.Unmarshal(r.Body, v) }

// ErrorCode extracts error.code from the standard error body.
func (r Response) ErrorCode() string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &e)
	return e.Error.Code
}

func (r Response) String() string {
	b := r.Body
	if len(b) > 300 {
		b = b[:300]
	}
	return fmt.Sprintf("HTTP %d %s", r.Status, bytes.TrimSpace(b))
}

// Do sends a request. headers may carry e.g. Idempotency-Key.
func (c *Client) Do(ctx context.Context, method, path string, body any, headers ...string) (Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return Response{}, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return Response{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return Response{Status: resp.StatusCode, Body: b, Headers: resp.Header}, err
}

// Login authenticates and stores the tokens on the client.
func (c *Client) Login(ctx context.Context, email, password string) (Response, error) {
	resp, err := c.Do(ctx, http.MethodPost, "/api/auth/login", map[string]string{"email": email, "password": password})
	if err != nil || resp.Status != http.StatusOK {
		return resp, err
	}
	var out struct {
		AccessToken  string         `json:"access_token"`
		RefreshToken string         `json:"refresh_token"`
		User         map[string]any `json:"user"`
	}
	if err := resp.JSON(&out); err != nil {
		return resp, err
	}
	c.Token, c.Refresh, c.User = out.AccessToken, out.RefreshToken, out.User
	return resp, nil
}

// Expect fails unless the response has the wanted status; it decodes into v when non-nil.
func Expect(resp Response, err error, want int, v any) error {
	if err != nil {
		return err
	}
	if resp.Status != want {
		return fmt.Errorf("beklenen HTTP %d, gelen %s", want, resp)
	}
	if v != nil {
		return resp.JSON(v)
	}
	return nil
}
