// Package argocd talks to the Argo CD REST API.
package argocd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

const requestTimeout = 30 * time.Second

// Client accesses Argo CD using a bearer token.
type Client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
}

// New creates a client for an absolute HTTP(S) URL, optionally with a base path.
func New(baseURL, token string, httpClient *http.Client) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("argocd base URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Opaque != "" {
		return nil, errors.New("argocd base URL must be an absolute http:// or https:// URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(baseURL, "#") {
		return nil, errors.New("argocd base URL must not contain credentials, a query or a fragment")
	}
	if token == "" || strings.ContainsAny(token, " \t\r\n\v\f") {
		return nil, errors.New("argocd token must be non-empty and contain no whitespace")
	}
	configured := http.Client{Timeout: requestTimeout}
	if httpClient != nil {
		configured = *httpClient
	}
	// A redirect must not resend a sync request or forward its credentials.
	configured.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{baseURL: u, token: token, httpClient: &configured}, nil
}

// doJSON makes one API request and decodes its response when out is non-nil.
func (c *Client) doJSON(ctx context.Context, method string, path []string, query url.Values, body, out any) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("argocd request: %w", err)
	}
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("argocd encode request: %w", err)
		}
		payload = bytes.NewReader(data)
	}
	u := *c.baseURL
	u.RawPath = strings.TrimRight(u.EscapedPath(), "/") + "/api/v1"
	for _, part := range path {
		u.RawPath += "/" + url.PathEscape(part)
	}
	u.Path, _ = url.PathUnescape(u.RawPath) // the base URL and each segment are valid escapes
	u.RawQuery = query.Encode()

	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, method, u.String(), payload)
	if err != nil {
		return fmt.Errorf("argocd create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return requestError(ctx, fmt.Errorf("argocd request: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := decodeResponseError(resp)
		if ctx.Err() != nil {
			return requestError(ctx, err)
		}
		return err
	}
	if out == nil {
		_, err := io.Copy(io.Discard, resp.Body)
		if err != nil {
			return requestError(ctx, fmt.Errorf("argocd read response: %w", err))
		}
		return nil
	}
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(out); err != nil {
		return decodeError(ctx, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("argocd decode response: multiple JSON values")
		}
		return decodeError(ctx, err)
	}
	return nil
}

func decodeError(ctx context.Context, err error) error {
	wrapped := fmt.Errorf("argocd decode response: %w", err)
	if ctx.Err() == nil && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
		return wrapped // empty or incomplete JSON is invalid response data
	}
	return requestError(ctx, wrapped)
}

// responseError retains the HTTP status and Argo CD message for callers.
type responseError struct {
	statusCode int
	message    string
}

func (e *responseError) Error() string {
	return fmt.Sprintf("argocd HTTP %d: %s", e.statusCode, e.message)
}

func decodeResponseError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var body struct {
		Message string `json:"message"`
	}
	message := strings.TrimSpace(string(data))
	if json.Unmarshal(data, &body) == nil && body.Message != "" {
		message = body.Message
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	err := &responseError{statusCode: resp.StatusCode, message: message}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: %w", err, steps.ErrNotFound)
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 && resp.StatusCode <= 599:
		return steps.Retryable(err)
	default:
		return err
	}
}

func requestError(ctx context.Context, err error) error {
	// Caller cancellation ends the work; a client timeout may be retried.
	if ctx.Err() != nil {
		return fmt.Errorf("argocd request: %w", ctx.Err())
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return steps.Retryable(err)
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return err
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return steps.Retryable(err)
	}
	return err
}
