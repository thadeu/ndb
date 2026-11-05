package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// apiClient talks to a running daemon over a unix socket or TCP.
type apiClient struct {
	base  string
	token string
	hc    *http.Client
}

// newAPIClient reads unix:///path/to.sock, tcp://host:port or
// http://host:port. The token is NDB_TOKEN.
func newAPIClient(rawURL string) (*apiClient, error) {
	token := os.Getenv("NDB_TOKEN")

	if sock, ok := strings.CutPrefix(rawURL, "unix://"); ok {
		return newUnixClient(sock, token), nil
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("url %q: %w", rawURL, err)
	}

	if (u.Scheme != "tcp" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("url %q: want unix:///path, tcp://host:port or http://host:port", rawURL)
	}

	return &apiClient{base: "http://" + u.Host, token: token, hc: &http.Client{Timeout: 10 * time.Second}}, nil
}

func newUnixClient(sock, token string) *apiClient {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer

			return d.DialContext(ctx, "unix", sock)
		},
	}

	return &apiClient{base: "http://ndb", token: token, hc: &http.Client{Transport: tr, Timeout: 10 * time.Second}}
}

// get decodes the JSON answer of GET path into v.
func (c *apiClient) get(path string, v any) error {
	req, err := http.NewRequest(http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %d %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return json.Unmarshal(body, v)
}
