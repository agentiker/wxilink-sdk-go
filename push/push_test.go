package push

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	ilink "github.com/agentiker/wxilink-sdk-go"
)

// fakeILink simulates the subset of the iLink API used by push.
type fakeILink struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	sent      []ilink.SendMessageReq
	auth      []string
	uploads   int
	sendReply string // body returned by sendmessage
	cfgReply  string // body returned by getconfig
}

func newFake(t *testing.T) *fakeILink {
	f := &fakeILink{t: t, sendReply: `{"message_id":1}`, cfgReply: `{"ret":0,"typing_ticket":"tt"}`}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeILink) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "get_bot_qrcode"):
		w.Write([]byte(`{"qrcode":"qr1","qrcode_img_content":"https://example.invalid/qr1"}`))
	case strings.HasSuffix(r.URL.Path, "get_qrcode_status"):
		w.Write([]byte(`{"status":"confirmed","bot_token":"tok-1","ilink_bot_id":"bot@im.bot","ilink_user_id":"me@im.wechat"}`))
	case strings.HasSuffix(r.URL.Path, "ilink/bot/sendmessage"):
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		var req ilink.SendMessageReq
		json.NewDecoder(r.Body).Decode(&req)
		f.sent = append(f.sent, req)
		w.Write([]byte(f.sendReply))
	case strings.HasSuffix(r.URL.Path, "ilink/bot/getconfig"):
		w.Write([]byte(f.cfgReply))
	case strings.HasSuffix(r.URL.Path, "ilink/bot/getuploadurl"):
		w.Write([]byte(`{"upload_full_url":"` + f.srv.URL + `/cdn/upload"}`))
	case r.URL.Path == "/cdn/upload":
		io.Copy(io.Discard, r.Body)
		f.uploads++
		w.Header().Set("x-encrypted-param", "enc-param")
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

func TestLogin(t *testing.T) {
	f := newFake(t)
	var qr string
	creds, err := Login(context.Background(), &LoginOptions{
		OnQRCode:      func(c string) { qr = c },
		ClientOptions: []ilink.Option{ilink.WithBaseURL(f.srv.URL)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if qr != "https://example.invalid/qr1" {
		t.Errorf("qr = %q", qr)
	}
	if creds.BotToken != "tok-1" || creds.BotID != "bot@im.bot" || creds.ILinkUserID != "me@im.wechat" {
		t.Errorf("creds = %+v", creds)
	}
	if creds.SavedAt.IsZero() {
		t.Error("SavedAt not set")
	}
}

func TestLogin_RequiresOnQRCode(t *testing.T) {
	if _, err := Login(context.Background(), nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "creds.json")
	in := &Credentials{BotToken: "tok", BotID: "b", BaseURL: "https://x", ILinkUserID: "u"}
	if err := in.Save(path); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(path)
		if st.Mode().Perm() != 0o600 {
			t.Errorf("perm = %v, want 0600", st.Mode().Perm())
		}
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.BotToken != in.BotToken || out.ILinkUserID != in.ILinkUserID || out.BaseURL != in.BaseURL {
		t.Errorf("round trip: got %+v", out)
	}
}

func TestLoad_Invalid(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(filepath.Join(dir, "missing.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: got %v", err)
	}
	p := filepath.Join(dir, "empty.json")
	os.WriteFile(p, []byte(`{}`), 0o600)
	if _, err := Load(p); err == nil {
		t.Error("expected error for missing bot_token")
	}
}

func TestText_DefaultRecipient(t *testing.T) {
	f := newFake(t)
	p := New(&Credentials{BotToken: "tok-1", BaseURL: f.srv.URL, ILinkUserID: "me@im.wechat"})
	id, err := p.Text(context.Background(), "", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Error("empty client_id")
	}
	if len(f.sent) != 1 {
		t.Fatalf("sent %d messages", len(f.sent))
	}
	m := f.sent[0].Msg
	if m.ToUserID != "me@im.wechat" || m.ItemList[0].TextItem.Text != "hello" {
		t.Errorf("msg = %+v", m)
	}
	if f.auth[0] != "Bearer tok-1" {
		t.Errorf("auth = %q", f.auth[0])
	}
}

func TestText_ExplicitRecipient(t *testing.T) {
	f := newFake(t)
	p := New(&Credentials{BotToken: "tok-1", BaseURL: f.srv.URL, ILinkUserID: "me@im.wechat"})
	if _, err := p.Text(context.Background(), "other@im.wechat", "hi"); err != nil {
		t.Fatal(err)
	}
	if f.sent[0].Msg.ToUserID != "other@im.wechat" {
		t.Errorf("to = %q", f.sent[0].Msg.ToUserID)
	}
}

func TestText_NoRecipient(t *testing.T) {
	p := New(&Credentials{BotToken: "tok-1"})
	if _, err := p.Text(context.Background(), "", "hi"); err == nil {
		t.Fatal("expected error")
	}
}

func TestText_NoSession(t *testing.T) {
	f := newFake(t)
	f.sendReply = `{"ret":-2,"errmsg":"prepare failed"}`
	p := New(&Credentials{BotToken: "tok-1", BaseURL: f.srv.URL, ILinkUserID: "me@im.wechat"})
	_, err := p.Text(context.Background(), "", "hi")
	var apiErr *ilink.APIError
	if !errors.As(err, &apiErr) || !apiErr.IsNoSession() {
		t.Fatalf("got %v, want IsNoSession", err)
	}
}

func TestFile_UploadsAndSends(t *testing.T) {
	f := newFake(t)
	p := New(&Credentials{BotToken: "tok-1", BaseURL: f.srv.URL, ILinkUserID: "me@im.wechat"})
	err := p.File(context.Background(), "", "report.pdf", []byte("%PDF-1.4 test"), "日报")
	if err != nil {
		t.Fatal(err)
	}
	if f.uploads != 1 {
		t.Errorf("uploads = %d", f.uploads)
	}
	// caption text first, then the file item
	if len(f.sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(f.sent))
	}
	if f.sent[0].Msg.ItemList[0].TextItem.Text != "日报" {
		t.Errorf("caption = %+v", f.sent[0].Msg.ItemList[0])
	}
	item := f.sent[1].Msg.ItemList[0]
	if item.Type != ilink.ItemFile || item.FileItem == nil || item.FileItem.FileName != "report.pdf" {
		t.Errorf("file item = %+v", item)
	}
}

func TestCheck(t *testing.T) {
	f := newFake(t)
	p := New(&Credentials{BotToken: "tok-1", BaseURL: f.srv.URL, ILinkUserID: "me@im.wechat"})
	if err := p.Check(context.Background(), ""); err != nil {
		t.Fatalf("ok case: %v", err)
	}
	f.cfgReply = `{"ret":-4,"errmsg":"GetTypingTicket rpc failed"}`
	var apiErr *ilink.APIError
	if err := p.Check(context.Background(), ""); !errors.As(err, &apiErr) || apiErr.Ret != -4 {
		t.Fatalf("no-session case: got %v", err)
	}
	if len(f.sent) != 0 {
		t.Error("Check must not send messages")
	}
}
