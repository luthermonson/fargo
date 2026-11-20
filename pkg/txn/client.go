// Package txn is a client for the kine control API v0: the scoped-transaction
// endpoints a fargo controller drives to make a rollout transactional
// (POST /txn/begin, GET /txn/{id}, POST /txn/{id}/commit,
// POST /txn/{id}/rollback, GET /txn). It speaks JSON over TCP or a unix
// socket, with an optional bearer token.
package txn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// State is the lifecycle state of a transaction in the kine ledger.
type State string

const (
	StateOpen       State = "open"
	StateCommitted  State = "committed"
	StateRolledBack State = "rolledBack"
)

// BeginRequest is the body of POST /txn/begin.
type BeginRequest struct {
	ID    string `json:"id"`
	Scope Scope  `json:"scope"`
}

// Transaction is the ledger record returned by the API.
type Transaction struct {
	ID         string          `json:"id"`
	State      State           `json:"state"`
	BaseRev    int64           `json:"baseRev"`
	BaseCommit string          `json:"baseCommit,omitempty"`
	StartedAt  string          `json:"startedAt,omitempty"`
	Scope      *Scope          `json:"scope,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
}

// RollbackResult is returned by POST /txn/{id}/rollback.
type RollbackResult struct {
	ID       string   `json:"id"`
	State    State    `json:"state"`
	Events   int      `json:"events"`
	FreezeMs int      `json:"freezeMs"`
	Reverted []string `json:"reverted"`
}

// Client talks to one kine control endpoint.
type Client struct {
	base       string
	token      string
	httpClient *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithToken sets the bearer token sent as Authorization on every request.
func WithToken(token string) Option {
	return func(c *Client) { c.token = strings.TrimSpace(token) }
}

// WithHTTPClient overrides the underlying http.Client (used by tests).
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpClient = h }
}

// New builds a Client for an endpoint. The endpoint is either a TCP URL
// (http://host:port) or a unix socket (unix:///path/kine.sock), so the same
// controller binary serves both the live rig's socket and a remote kine.
func New(endpoint string, opts ...Option) (*Client, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("kine endpoint is empty")
	}
	c := &Client{}
	if strings.HasPrefix(endpoint, "unix://") {
		sock := strings.TrimPrefix(endpoint, "unix://")
		if sock == "" {
			return nil, fmt.Errorf("unix endpoint %q has no socket path", endpoint)
		}
		c.base = "http://unix"
		c.httpClient = &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", sock)
				},
			},
		}
	} else {
		if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			return nil, fmt.Errorf("kine endpoint %q must be http://, https:// or unix://", endpoint)
		}
		c.base = strings.TrimRight(endpoint, "/")
		c.httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// apiError carries a JSON {"error": "..."} body and the HTTP status.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("kine control API: HTTP %d", e.Status)
	}
	return fmt.Sprintf("kine control API: HTTP %d: %s", e.Status, e.Message)
}

// IsConflict reports whether err is a 409 (overlapping open transaction).
func IsConflict(err error) bool {
	ae, ok := err.(*apiError)
	return ok && ae.Status == http.StatusConflict
}

// IsNotFound reports whether err is a 404 (no such transaction in the ledger).
func IsNotFound(err error) bool {
	ae, ok := err.(*apiError)
	return ok && ae.Status == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		var je struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &je) == nil && je.Error != "" {
			msg = je.Error
		}
		return &apiError{Status: resp.StatusCode, Message: msg}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// Begin opens a scoped transaction. A 409 (IsConflict) means the scope
// overlaps an already-open transaction.
func (c *Client) Begin(ctx context.Context, id string, scope Scope) (*Transaction, error) {
	var t Transaction
	if err := c.do(ctx, http.MethodPost, "/txn/begin", BeginRequest{ID: id, Scope: scope}, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// Get reads one transaction from the ledger. A 404 (IsNotFound) means the id
// was never begun.
func (c *Client) Get(ctx context.Context, id string) (*Transaction, error) {
	var t Transaction
	if err := c.do(ctx, http.MethodGet, "/txn/"+id, nil, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// List returns the open/finished transactions the ledger knows.
func (c *Client) List(ctx context.Context) ([]Transaction, error) {
	var ts []Transaction
	if err := c.do(ctx, http.MethodGet, "/txn", nil, &ts); err != nil {
		return nil, err
	}
	return ts, nil
}

// Commit forgets the savepoint; the writes were on main all along.
func (c *Client) Commit(ctx context.Context, id string) (*Transaction, error) {
	var t Transaction
	if err := c.do(ctx, http.MethodPost, "/txn/"+id+"/commit", nil, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// Rollback emits the inverse of the in-scope changes as fresh revisions.
func (c *Client) Rollback(ctx context.Context, id string) (*RollbackResult, error) {
	var r RollbackResult
	if err := c.do(ctx, http.MethodPost, "/txn/"+id+"/rollback", nil, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
