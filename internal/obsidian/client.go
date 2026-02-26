package obsidian

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	authHeader string
	apiKey     string
	httpClient *http.Client
}

type Health struct {
	Authenticated bool `json:"authenticated"`
}

func New(baseURL, authHeader, apiKey string, verifyTLS bool) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if !verifyTLS {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}

	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		authHeader: authHeader,
		apiKey:     apiKey,
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
	}
}

func (c *Client) Health(ctx context.Context) (Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/", nil)
	if err != nil {
		return Health{}, err
	}
	resp, err := c.do(req)
	if err != nil {
		return Health{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return Health{}, fmt.Errorf("obsidian health failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var health Health
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return Health{}, err
	}
	return health, nil
}

func (c *Client) FileExists(ctx context.Context, vaultPath string) (bool, error) {
	endpoint := c.baseURL + "/vault/" + encodeVaultPath(vaultPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	resp, err := c.do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return false, fmt.Errorf("obsidian file exists check failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
}

func (c *Client) CreateFile(ctx context.Context, vaultPath, content string) error {
	endpoint := c.baseURL + "/vault/" + encodeVaultPath(vaultPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, strings.NewReader(content))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/markdown; charset=utf-8")
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("obsidian create file failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *Client) AppendFile(ctx context.Context, vaultPath, content string) error {
	endpoint := c.baseURL + "/vault/" + encodeVaultPath(vaultPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(content))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/markdown; charset=utf-8")
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("obsidian append failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *Client) ReadFile(ctx context.Context, vaultPath string) (string, error) {
	endpoint := c.baseURL + "/vault/" + encodeVaultPath(vaultPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("obsidian read failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func encodeVaultPath(vaultPath string) string {
	parts := strings.Split(strings.TrimPrefix(vaultPath, "/"), "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	if c.authHeader != "" {
		req.Header.Set(c.authHeader, "Bearer "+c.apiKey)
	}
	return c.httpClient.Do(req)
}
