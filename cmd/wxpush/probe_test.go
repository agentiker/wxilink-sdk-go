package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agentiker/wxilink-sdk-go/push"
)

func TestProbe_StopsAtFirstRefusal(t *testing.T) {
	var sends atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendmessage") {
			http.NotFound(w, r)
			return
		}
		if sends.Add(1) <= 3 {
			w.Write([]byte(`{"message_id":1}`))
			return
		}
		w.Write([]byte(`{"ret":-2,"errmsg":"prepare failed"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	creds := filepath.Join(dir, "creds.json")
	(&push.Credentials{BotToken: "tok", BaseURL: srv.URL, ILinkUserID: "me@im.wechat"}).Save(creds)
	logPath := filepath.Join(dir, "probe.jsonl")

	if err := cmdProbe(context.Background(), []string{"-creds", creds, "-interval", "1s", "-log", logPath}); err != nil {
		t.Fatal(err)
	}
	if n := sends.Load(); n != 4 {
		t.Errorf("sends = %d, want 4 (3 accepted + 1 refused)", n)
	}

	f, _ := os.Open(logPath)
	defer f.Close()
	var recs []probeRecord
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r probeRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r)
	}
	if len(recs) != 4 {
		t.Fatalf("records = %d, want 4", len(recs))
	}
	for i, r := range recs[:3] {
		if !r.OK || r.Seq != i+1 {
			t.Errorf("rec %d = %+v, want ok seq=%d", i, r, i+1)
		}
	}
	if last := recs[3]; last.OK || last.Ret != -2 || last.Seq != 4 {
		t.Errorf("last = %+v, want refused ret=-2 seq=4", last)
	}
}

func TestProbe_Validation(t *testing.T) {
	for _, args := range [][]string{{"-interval", "100ms"}, {"-max", "0"}} {
		err := cmdProbe(context.Background(), args)
		if _, ok := err.(usageError); !ok {
			t.Errorf("args %v: got %v, want usageError", args, err)
		}
	}
}
