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
// allows 10 messages per user message (see package quota). After login,
// send any message to the bot from WeChat before pushing; refused sends fail
// with an *ilink.APIError where IsNoSession() is true. Sending the bot
// another message from WeChat restores the allowance.
//
// With [Pusher.UseQuota], the Pusher counts sends and puts a "reply to keep
// receiving" nudge into the last allowed message, so the recipient knows
// what to do instead of silently missing notifications.
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
	"github.com/agentiker/wxilink-sdk-go/quota"
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
	quota  *quota.Counter
}

// ErrQuotaDeferred is returned by [Pusher.File] when only the last message
// of the allowance is left: the nudge was sent instead of the file, which
// should be retried after the recipient replies.
var ErrQuotaDeferred = errors.New("push: send allowance nearly used up; sent a reply-request instead of the file, retry after the recipient replies")

// UseQuota enables send counting with c (typically loaded from and saved to
// a file between runs). Pass nil to disable.
//
// A Pusher cannot see inbound messages, so it infers a reset: when the
// counter says the allowance is used up but a send succeeds, the recipient
// must have replied in the meantime.
func (p *Pusher) UseQuota(c *quota.Counter) { p.quota = c }

// Quota returns the counter set with UseQuota, or nil.
func (p *Pusher) Quota() *quota.Counter { return p.quota }

// account updates the counter after a send attempt that would cost n.
func (p *Pusher) account(to string, believedRemaining, n int, err error) {
	if p.quota == nil {
		return
	}
	if err == nil {
		if believedRemaining <= 0 {
			p.quota.Inbound(to) // must have replied; inferred reset
		}
		p.quota.Sent(to, n)
		return
	}
	var ae *ilink.APIError
	if errors.As(err, &ae) && ae.IsNoSession() {
		p.quota.Refused(to)
	}
}

func (p *Pusher) remaining(to string) int {
	if p.quota == nil {
		return quota.Limit // unlimited as far as we know
	}
	return p.quota.Remaining(to)
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
// Returns the client_id of the sent message. With UseQuota, the last
// allowed message gets the reply nudge appended.
func (p *Pusher) Text(ctx context.Context, to, text string) (string, error) {
	to, err := p.recipient(to)
	if err != nil {
		return "", err
	}
	rem := p.remaining(to)
	if p.quota != nil && rem == 1 {
		text = quota.WithNudge(text, 0)
	}
	id, err := p.client.Push(ctx, to, text)
	p.account(to, rem, 1, err)
	return id, err
}

// File uploads data and sends it as an image, video or file attachment,
// chosen from fileName's extension. A non-empty caption is sent first as a
// separate text message.
//
// With UseQuota: if the file is the last allowed message, the nudge goes
// into the caption (cost 2 needs 2 left); if only 1 is left the nudge is
// sent instead of the file and [ErrQuotaDeferred] is returned.
func (p *Pusher) File(ctx context.Context, to, fileName string, data []byte, caption string) error {
	to, err := p.recipient(to)
	if err != nil {
		return err
	}
	rem := p.remaining(to)
	if p.quota != nil && rem > 0 {
		cost := quota.Cost(true, caption != "")
		switch {
		case rem == 1:
			_, err := p.client.Push(ctx, to, quota.Nudge(1))
			p.account(to, rem, 1, err)
			if err != nil {
				return err
			}
			return ErrQuotaDeferred
		case rem-cost <= 0:
			// This file uses up the allowance: carry the nudge in the caption.
			caption = quota.WithNudge(caption, 0)
		}
	}
	token, _ := p.client.GetContextToken(to)
	err = p.client.SendMediaFile(ctx, to, token, data, fileName, caption)
	p.account(to, rem, quota.Cost(true, caption != ""), err)
	return err
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
