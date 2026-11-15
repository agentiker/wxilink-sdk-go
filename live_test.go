//go:build live

// Live tests against the real iLink server. They send visible WeChat
// messages, so they are excluded from normal runs.
//
//	WXILINK_LIVE_CREDS=~/.config/wxilink/credentials.json go test -tags live -run Live -v .
//
// Requirements: the credentials' ilink_user_id (or WXILINK_LIVE_TO) must
// have messaged the bot shortly before. iLink limits how many messages the
// bot may send per user message (observed: 10) and a full run sends 6
// (image caption counts), so message the bot from WeChat before each run.
//
// These tests pin down iLink behaviour the SDK relies on but that is not
// documented by Tencent. If one starts failing, the protocol changed.
package ilink_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	ilink "github.com/agentiker/wxilink-sdk-go"
)

type liveCreds struct {
	BotToken    string `json:"bot_token"`
	BaseURL     string `json:"base_url"`
	ILinkUserID string `json:"ilink_user_id"`
}

func liveSetup(t *testing.T) (*ilink.Client, liveCreds, context.Context) {
	t.Helper()
	path := os.Getenv("WXILINK_LIVE_CREDS")
	if path == "" {
		t.Skip("WXILINK_LIVE_CREDS not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c liveCreds
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	if to := os.Getenv("WXILINK_LIVE_TO"); to != "" {
		c.ILinkUserID = to
	}
	var opts []ilink.Option
	if c.BaseURL != "" {
		opts = append(opts, ilink.WithBaseURL(c.BaseURL))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ilink.NewClient(c.BotToken, opts...), c, ctx
}

func stamp(label string) string {
	return "[wxilink live] " + label + " " + time.Now().Format("15:04:05")
}

// The server accepts sends with no context_token once a session exists.
func TestLive_SendWithoutContextToken(t *testing.T) {
	c, cr, ctx := liveSetup(t)
	if _, err := c.SendText(ctx, cr.ILinkUserID, stamp("no context_token"), ""); err != nil {
		t.Fatalf("send without token: %v", err)
	}
}

// The server does not validate context_token contents.
func TestLive_SendWithBogusContextToken(t *testing.T) {
	c, cr, ctx := liveSetup(t)
	if _, err := c.SendText(ctx, cr.ILinkUserID, stamp("bogus context_token"), "AARzJWAFAAABAAAAAAAbogus=="); err != nil {
		t.Fatalf("send with bogus token: %v", err)
	}
}

// Push works without a cached token.
func TestLive_PushWithoutCache(t *testing.T) {
	c, cr, ctx := liveSetup(t)
	if _, err := c.Push(ctx, cr.ILinkUserID, stamp("Push")); err != nil {
		t.Fatalf("push: %v", err)
	}
}

// Unknown recipients are rejected in-band (HTTP 200) with ret=-3.
func TestLive_InvalidRecipient(t *testing.T) {
	c, _, ctx := liveSetup(t)
	_, err := c.SendText(ctx, "wxilink_nonexistent_user@im.wechat", "x", "")
	var ae *ilink.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("got %v, want *APIError", err)
	}
	if !ae.IsInvalidArgument() {
		t.Errorf("got ret=%d errcode=%d %q, want ret=-3", ae.Ret, ae.ErrCode, ae.ErrMsg)
	}
}

// An invalid bot token yields errcode=-14.
func TestLive_InvalidBotToken(t *testing.T) {
	_, cr, ctx := liveSetup(t)
	bad := ilink.NewClient("wxilink_invalid_token", ilink.WithBaseURL(cr.BaseURL))
	_, err := bad.SendText(ctx, cr.ILinkUserID, "x", "")
	var ae *ilink.APIError
	if !errors.As(err, &ae) || !ae.IsSessionExpired() {
		t.Fatalf("got %v, want IsSessionExpired", err)
	}
}

// getconfig succeeds while a session exists (used by push.Check).
func TestLive_GetConfig(t *testing.T) {
	c, cr, ctx := liveSetup(t)
	if _, err := c.GetConfig(ctx, cr.ILinkUserID, ""); err != nil {
		t.Fatalf("getconfig: %v", err)
	}
}

// Media upload via CDN + send. Exercises getuploadurl, AES-128-ECB, CDN POST.
func TestLive_SendImageAndFile(t *testing.T) {
	c, cr, ctx := liveSetup(t)
	// 1x1 PNG
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde\x00\x00\x00\x0cIDATx\x9cc\xf8\xcf\xc0\x00\x00\x03\x01\x01\x00\xc9\xfe\x92\xef\x00\x00\x00\x00IEND\xaeB`\x82")
	if err := c.SendMediaFile(ctx, cr.ILinkUserID, "", png, "wxilink-live.png", stamp("image")); err != nil {
		t.Fatalf("image: %v", err)
	}
	if err := c.SendMediaFile(ctx, cr.ILinkUserID, "", []byte(stamp("file body")), "wxilink-live.txt", ""); err != nil {
		t.Fatalf("file: %v", err)
	}
}
