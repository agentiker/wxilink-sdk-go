// Package push is a minimal "scan once, push anytime" layer over the ilink
// client, for sending notifications to WeChat without running a full bot.
//
//	creds, _ := push.Login(ctx, &push.LoginOptions{OnQRCode: showQR})
//	creds.Save(path)
//	...
//	p := push.New(creds)
//	p.Text(ctx, "", "build finished") // "" = the WeChat user who scanned
//
// iLink only delivers to users who have recently messaged the bot, and
// limits how many messages the bot may send per user message (observed: 10).
// After login, send any message to the bot from WeChat before pushing;
// refused sends fail with an *ilink.APIError where IsNoSession() is true.
// Sending the bot another message from WeChat restores the allowance.
package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	ilink "github.com/agentiker/wxilink-sdk-go"
)

// Credentials are the long-lived result of a QR login.
type Credentials struct {
	BotToken string `json:"bot_token"`
	BotID    string `json:"bot_id"`
	BaseURL  string `json:"base_url,omitempty"`
	// ILinkUserID is the WeChat user who scanned the QR code. It is the
	// default recipient when no explicit recipient is given.
	ILinkUserID string    `json:"ilink_user_id"`
	SavedAt     time.Time `json:"saved_at"`
}

// DefaultPath returns <UserConfigDir>/wxilink/credentials.json.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "wxilink", "credentials.json"), nil
}

// Load reads credentials from path.
func Load(path string) (*Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("push: parse %s: %w", path, err)
	}
	if c.BotToken == "" {
		return nil, fmt.Errorf("push: %s has no bot_token", path)
	}
	return &c, nil
}

// Save writes credentials to path with 0600 permissions, creating parent
// directories (0700) as needed. The bot token is a secret.
func (c *Credentials) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if c.SavedAt.IsZero() {
		c.SavedAt = time.Now()
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoginOptions configures [Login].
type LoginOptions struct {
	// OnQRCode receives the content to render as a QR code (a URL).
	// Required: the user must scan it with WeChat.
	OnQRCode func(content string)
	// OnScanned is called once the QR code has been scanned.
	OnScanned func()
	// OnExpired is called when the QR code is refreshed.
	OnExpired func(attempt, max int)
	// ClientOptions are passed to ilink.NewClient (e.g. WithBaseURL in tests).
	ClientOptions []ilink.Option
}

// ErrLoginFailed is returned when the QR flow ends without a connection
// (timeout, too many expirations, ...). The wrapped message has details.
var ErrLoginFailed = errors.New("push: login failed")

// Login runs the QR login flow and returns the resulting credentials.
func Login(ctx context.Context, opts *LoginOptions) (*Credentials, error) {
	if opts == nil || opts.OnQRCode == nil {
		return nil, errors.New("push: LoginOptions.OnQRCode is required")
	}
	client := ilink.NewClient("", opts.ClientOptions...)
	res, err := client.LoginWithQR(ctx, &ilink.LoginCallbacks{
		OnQRCode:  opts.OnQRCode,
		OnScanned: opts.OnScanned,
		OnExpired: opts.OnExpired,
	})
	if err != nil {
		return nil, err
	}
	if !res.Connected {
		return nil, fmt.Errorf("%w: %s", ErrLoginFailed, res.Message)
	}
	return &Credentials{
		BotToken:    res.BotToken,
		BotID:       res.BotID,
		BaseURL:     res.BaseURL,
		ILinkUserID: res.UserID,
		SavedAt:     time.Now(),
	}, nil
}

// Pusher sends messages using saved credentials.
type Pusher struct {
	client *ilink.Client
	creds  *Credentials
}

// New creates a Pusher. Extra options are applied after the base URL from
// the credentials, so they can override it.
func New(creds *Credentials, opts ...ilink.Option) *Pusher {
	var all []ilink.Option
	if creds.BaseURL != "" {
		all = append(all, ilink.WithBaseURL(creds.BaseURL))
	}
	all = append(all, opts...)
	return &Pusher{client: ilink.NewClient(creds.BotToken, all...), creds: creds}
}

// Client exposes the underlying ilink client for advanced use.
func (p *Pusher) Client() *ilink.Client { return p.client }

// Credentials returns the credentials the Pusher was created with.
func (p *Pusher) Credentials() *Credentials { return p.creds }

func (p *Pusher) recipient(to string) (string, error) {
	if to != "" {
		return to, nil
	}
	if p.creds.ILinkUserID == "" {
		return "", errors.New("push: no recipient and credentials have no ilink_user_id")
	}
	return p.creds.ILinkUserID, nil
}

// Text sends a text message. An empty to means the user who scanned the QR.
// Returns the client_id of the sent message.
func (p *Pusher) Text(ctx context.Context, to, text string) (string, error) {
	to, err := p.recipient(to)
	if err != nil {
		return "", err
	}
	return p.client.Push(ctx, to, text)
}

// File uploads data and sends it as an image, video or file attachment,
// chosen from fileName's extension. A non-empty caption is sent first as a
// separate text message.
func (p *Pusher) File(ctx context.Context, to, fileName string, data []byte, caption string) error {
	to, err := p.recipient(to)
	if err != nil {
		return err
	}
	token, _ := p.client.GetContextToken(to)
	return p.client.SendMediaFile(ctx, to, token, data, fileName, caption)
}

// Check verifies, without sending a visible message, that the bot token is
// valid and the recipient has messaged the bot at some point. It calls
// getconfig, which fails (ret=-4) when the recipient never interacted.
//
// Check does NOT guarantee the next send is accepted: getconfig keeps
// succeeding after the per-message send allowance is used up (verified
// live). Only an actual send tells.
func (p *Pusher) Check(ctx context.Context, to string) error {
	to, err := p.recipient(to)
	if err != nil {
		return err
	}
	token, _ := p.client.GetContextToken(to)
	_, err = p.client.GetConfig(ctx, to, token)
	return err
}
