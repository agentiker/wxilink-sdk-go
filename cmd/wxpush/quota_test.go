package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	ilink "github.com/agentiker/wxilink-sdk-go"
	"github.com/agentiker/wxilink-sdk-go/push"
	"github.com/agentiker/wxilink-sdk-go/quota"
)

func TestSend_QuotaPersistsAcrossInvocations(t *testing.T) {
	var mu sync.Mutex
	var texts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ilink.SendMessageReq
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		texts = append(texts, req.Msg.ItemList[0].TextItem.Text)
		mu.Unlock()
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	creds := filepath.Join(t.TempDir(), "creds.json")
	(&push.Credentials{BotToken: "tok", BaseURL: srv.URL, ILinkUserID: "me"}).Save(creds)

	for i := 0; i < 10; i++ { // each call is a separate "process"
		if err := cmdSend(context.Background(), []string{"-creds", creds, "n"}); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(texts[8], "💬") || !strings.Contains(texts[9], "回复任意消息") {
		t.Errorf("9th=%q 10th=%q, want nudge only on the 10th", texts[8], texts[9])
	}
	c, _ := quota.Load(quotaPath(creds))
	if c.Remaining("me") != 0 {
		t.Errorf("persisted remaining = %d, want 0", c.Remaining("me"))
	}

	// After the recipient replies the user can reset; next 9 have no nudge.
	if err := cmdQuota([]string{"-creds", creds, "-reset"}); err != nil {
		t.Fatal(err)
	}
	c, _ = quota.Load(quotaPath(creds))
	if c.Remaining("me") != 10 {
		t.Errorf("after reset = %d", c.Remaining("me"))
	}

	// -no-quota: no counting, no nudge.
	texts = nil
	for i := 0; i < 10; i++ {
		cmdSend(context.Background(), []string{"-creds", creds, "-no-quota", "x"})
	}
	for _, s := range texts {
		if strings.Contains(s, "💬") {
			t.Fatal("-no-quota must not append the nudge")
		}
	}
	c, _ = quota.Load(quotaPath(creds))
	if c.Remaining("me") != 10 {
		t.Errorf("-no-quota must not count, remaining = %d", c.Remaining("me"))
	}
}

func TestQuotaPath(t *testing.T) {
	if got := quotaPath("/a/credentials.json"); got != "/a/credentials.quota.json" {
		t.Errorf("got %q", got)
	}
}
