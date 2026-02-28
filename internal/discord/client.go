package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Client struct {
	httpClient *http.Client
	token      string
	baseURL    string
}

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

type Attachment struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
}

type Message struct {
	ID          string       `json:"id"`
	ChannelID   string       `json:"channel_id"`
	GuildID     string       `json:"guild_id"`
	Timestamp   string       `json:"timestamp"`
	Content     string       `json:"content"`
	Author      User         `json:"author"`
	Attachments []Attachment `json:"attachments"`
}

type Channel struct {
	ID      string `json:"id"`
	GuildID string `json:"guild_id"`
}

func New(token string) *Client {
	return NewWithBaseURL(token, "https://discord.com/api/v10")
}

func NewWithBaseURL(token, baseURL string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		token:      token,
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

func (c *Client) Me(ctx context.Context) (User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/users/@me", nil)
	if err != nil {
		return User{}, err
	}
	resp, err := c.do(req)
	if err != nil {
		return User{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return User{}, fmt.Errorf("discord me failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return User{}, err
	}
	return user, nil
}

func (c *Client) FetchMessages(ctx context.Context, channelID, after string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 100 {
		limit = 100
	}
	query := url.Values{}
	query.Set("limit", fmt.Sprintf("%d", limit))
	if strings.TrimSpace(after) != "" {
		query.Set("after", after)
	}
	endpoint := fmt.Sprintf("%s/channels/%s/messages?%s", c.baseURL, channelID, query.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("discord fetch messages failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var messages []Message
	if err := json.NewDecoder(resp.Body).Decode(&messages); err != nil {
		return nil, err
	}

	sort.Slice(messages, func(i, j int) bool {
		return compareSnowflake(messages[i].ID, messages[j].ID) < 0
	})
	return messages, nil
}

func (c *Client) AddReaction(ctx context.Context, channelID, messageID, emojiEscaped string) error {
	endpoint := fmt.Sprintf("%s/channels/%s/messages/%s/reactions/%s/@me", c.baseURL, channelID, messageID, emojiEscaped)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("discord add reaction failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *Client) GetChannel(ctx context.Context, channelID string) (Channel, error) {
	endpoint := fmt.Sprintf("%s/channels/%s", c.baseURL, channelID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Channel{}, err
	}
	resp, err := c.do(req)
	if err != nil {
		return Channel{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Channel{}, fmt.Errorf("discord get channel failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var ch Channel
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return Channel{}, err
	}
	return ch, nil
}

func (c *Client) DownloadAttachment(ctx context.Context, sourceURL, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("discord attachment download failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func compareSnowflake(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bot "+c.token)
	return c.httpClient.Do(req)
}

func IsAudioContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	return strings.HasPrefix(ct, "audio/")
}
