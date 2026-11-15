// Command wxpush pushes WeChat notifications through an iLink bot.
//
//	wxpush login                     # scan QR with WeChat, saves credentials
//	                                 # then send any message to the bot in WeChat
//	wxpush send "build ok"           # text to yourself
//	echo hi | wxpush send -          # text from stdin
//	wxpush file -caption 日报 a.pdf  # image / video / file by extension
//	wxpush check                     # silent token/interaction check (no message sent)
//	wxpush probe -interval 10s       # measure the send allowance (sends real messages)
//
// Credentials: -creds flag > $WXILINK_CREDS > <UserConfigDir>/wxilink/credentials.json
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	ilink "github.com/agentiker/wxilink-sdk-go"
	"github.com/agentiker/wxilink-sdk-go/push"
	"rsc.io/qr"
)

const (
	exitOK        = 0
	exitErr       = 1
	exitUsage     = 2
	exitNoSession = 3
)

const usage = `wxpush - push WeChat notifications via iLink

Usage:
  wxpush login  [-creds PATH]
  wxpush send   [-creds PATH] [-to USER] TEXT...   (TEXT "-" reads stdin)
  wxpush file   [-creds PATH] [-to USER] [-caption TEXT] FILE
  wxpush check  [-creds PATH] [-to USER]
  wxpush whoami [-creds PATH]
  wxpush probe  [-creds PATH] [-to USER] [-interval 10s] [-max 50] [-log FILE]

USER defaults to the WeChat account that scanned the QR code.
The recipient must have sent at least one message to the bot.

Exit codes: 0 ok, 1 error, 2 usage, 3 no session (user must message the bot)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(exitUsage)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "login":
		err = cmdLogin(ctx, args)
	case "send":
		err = cmdSend(ctx, args)
	case "file":
		err = cmdFile(ctx, args)
	case "check":
		err = cmdCheck(ctx, args)
	case "whoami":
		err = cmdWhoami(args)
	case "probe":
		err = cmdProbe(ctx, args)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(exitUsage)
	}
	os.Exit(exitCode(err))
}

func exitCode(err error) int {
	if err == nil {
		return exitOK
	}
	var ue usageError
	if errors.As(err, &ue) {
		fmt.Fprintln(os.Stderr, "wxpush:", err)
		return exitUsage
	}
	fmt.Fprintln(os.Stderr, "wxpush:", describe(err))
	var ae *ilink.APIError
	if errors.As(err, &ae) && (ae.IsNoSession() || ae.Ret == -4) {
		return exitNoSession
	}
	return exitErr
}

// describe adds a human hint to known iLink errors.
func describe(err error) string {
	var ae *ilink.APIError
	if !errors.As(err, &ae) {
		return err.Error()
	}
	switch {
	case ae.IsNoSession(), ae.Ret == -4:
		return err.Error() + "\n  hint: the recipient has no session with the bot; send any message to the bot in WeChat first"
	case ae.IsInvalidArgument():
		return err.Error() + "\n  hint: invalid recipient or parameters"
	case ae.IsSessionExpired():
		return err.Error() + "\n  hint: bot token expired; run `wxpush login` again"
	}
	return err.Error()
}

type usageError string

func (e usageError) Error() string { return string(e) }

// --- common flags ---

type common struct {
	fs    *flag.FlagSet
	creds string
	to    string
}

func newCommon(name string, withTo bool) *common {
	c := &common{fs: flag.NewFlagSet(name, flag.ContinueOnError)}
	c.fs.StringVar(&c.creds, "creds", "", "credentials file")
	if withTo {
		c.fs.StringVar(&c.to, "to", "", "recipient ilink user id (default: the account that scanned)")
	}
	return c
}

func (c *common) credsPath() (string, error) {
	if c.creds != "" {
		return c.creds, nil
	}
	if p := os.Getenv("WXILINK_CREDS"); p != "" {
		return p, nil
	}
	return push.DefaultPath()
}

func (c *common) pusher() (*push.Pusher, error) {
	path, err := c.credsPath()
	if err != nil {
		return nil, err
	}
	creds, err := push.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no credentials at %s; run `wxpush login` first", path)
	}
	if err != nil {
		return nil, err
	}
	return push.New(creds), nil
}

// --- commands ---

func cmdLogin(ctx context.Context, args []string) error {
	c := newCommon("login", false)
	if err := c.fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	path, err := c.credsPath()
	if err != nil {
		return err
	}
	creds, err := push.Login(ctx, &push.LoginOptions{
		OnQRCode: func(content string) {
			fmt.Fprintln(os.Stderr, "Scan with WeChat:")
			if err := printQR(os.Stderr, content); err != nil {
				fmt.Fprintln(os.Stderr, "  (cannot render QR:", err, ")")
			}
			fmt.Fprintln(os.Stderr, "  or open:", content)
		},
		OnScanned: func() { fmt.Fprintln(os.Stderr, "Scanned, confirm on your phone...") },
		OnExpired: func(a, m int) { fmt.Fprintf(os.Stderr, "QR expired, refreshing (%d/%d)\n", a, m) },
	})
	if err != nil {
		return err
	}
	if err := creds.Save(path); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Logged in: bot=%s user=%s\nSaved to %s\n", creds.BotID, creds.ILinkUserID, path)
	fmt.Fprintln(os.Stderr, "Next: send any message to the bot in WeChat, then try `wxpush send hello`.")
	return nil
}

func cmdSend(ctx context.Context, args []string) error {
	c := newCommon("send", true)
	if err := c.fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	text := strings.Join(c.fs.Args(), " ")
	if text == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		text = strings.TrimRight(string(b), "\n")
	}
	if strings.TrimSpace(text) == "" {
		return usageError("send: TEXT is required")
	}
	p, err := c.pusher()
	if err != nil {
		return err
	}
	id, err := p.Text(ctx, c.to, text)
	if err != nil {
		return err
	}
	fmt.Println(id)
	return nil
}

func cmdFile(ctx context.Context, args []string) error {
	c := newCommon("file", true)
	caption := c.fs.String("caption", "", "text sent before the file")
	if err := c.fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if c.fs.NArg() != 1 {
		return usageError("file: exactly one FILE is required")
	}
	path := c.fs.Arg(0)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	p, err := c.pusher()
	if err != nil {
		return err
	}
	return p.File(ctx, c.to, filepath.Base(path), data, *caption)
}

func cmdCheck(ctx context.Context, args []string) error {
	c := newCommon("check", true)
	if err := c.fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	p, err := c.pusher()
	if err != nil {
		return err
	}
	if err := p.Check(ctx, c.to); err != nil {
		return err
	}
	fmt.Println("ok")
	return nil
}

func cmdWhoami(args []string) error {
	c := newCommon("whoami", false)
	if err := c.fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	p, err := c.pusher()
	if err != nil {
		return err
	}
	cr := p.Credentials()
	path, _ := c.credsPath()
	fmt.Printf("bot_id:        %s\nilink_user_id: %s\nbase_url:      %s\nsaved_at:      %s\ncreds:         %s\n",
		cr.BotID, cr.ILinkUserID, cr.BaseURL, cr.SavedAt.Format(time.RFC3339), path)
	return nil
}

// probeRecord is one JSON line written by `wxpush probe`.
type probeRecord struct {
	Time    time.Time `json:"time"`
	Elapsed string    `json:"elapsed"` // since probe start
	Seq     int       `json:"seq"`     // 1-based send attempt
	OK      bool      `json:"ok"`
	Ret     int       `json:"ret,omitempty"`
	ErrCode int       `json:"errcode,omitempty"`
	ErrMsg  string    `json:"errmsg,omitempty"`
	Error   string    `json:"error,omitempty"` // transport errors
}

// cmdProbe sends numbered messages at a fixed interval until iLink refuses
// one, then reports how many were accepted and over how long. Run it right
// after messaging the bot from WeChat.
//
// Every round sends a real, visible message: getconfig cannot be used as a
// silent probe because it keeps succeeding after sends are refused.
// A short interval measures the per-message allowance; a long interval
// measures the time window (but the allowance may run out first).
func cmdProbe(ctx context.Context, args []string) error {
	c := newCommon("probe", true)
	interval := c.fs.Duration("interval", 10*time.Second, "time between sends")
	max := c.fs.Int("max", 50, "stop after this many accepted sends")
	logPath := c.fs.String("log", "", "append JSON lines to FILE (default: stdout)")
	if err := c.fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if *interval < time.Second {
		return usageError("probe: -interval must be >= 1s")
	}
	if *max < 1 {
		return usageError("probe: -max must be >= 1")
	}
	p, err := c.pusher()
	if err != nil {
		return err
	}
	out := io.Writer(os.Stdout)
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	w := bufio.NewWriter(out)
	enc := json.NewEncoder(w)
	start := time.Now()
	accepted := 0

	fmt.Fprintf(os.Stderr, "probing: one message every %s, up to %d; Ctrl-C to stop\n", *interval, *max)
	for seq := 1; ; seq++ {
		_, err := p.Text(ctx, c.to, fmt.Sprintf("[wxpush probe #%d] +%s", seq, time.Since(start).Round(time.Second)))
		r := probeRecord{Time: time.Now(), Elapsed: time.Since(start).Round(time.Second).String(), Seq: seq, OK: err == nil}
		var ae *ilink.APIError
		if errors.As(err, &ae) {
			r.Ret, r.ErrCode, r.ErrMsg = ae.Ret, ae.ErrCode, ae.ErrMsg
		} else if err != nil {
			r.Error = err.Error()
		}
		enc.Encode(r)
		w.Flush()

		if err == nil {
			accepted++
		}
		if ae != nil { // explicit refusal ends the probe; transport errors are retried
			fmt.Fprintf(os.Stderr, "refused at #%d after %s: %d accepted (%s)\n",
				seq, time.Since(start).Round(time.Second), accepted, describe(err))
			return nil
		}
		if accepted >= *max {
			fmt.Fprintf(os.Stderr, "reached -max: %d accepted over %s, no refusal\n", accepted, time.Since(start).Round(time.Second))
			return nil
		}
		select {
		case <-ctx.Done():
			fmt.Fprintf(os.Stderr, "interrupted: %d accepted over %s\n", accepted, time.Since(start).Round(time.Second))
			return nil
		case <-time.After(*interval):
		}
	}
}

// printQR renders content as a QR code with Unicode half blocks.
func printQR(w io.Writer, content string) error {
	code, err := qr.Encode(content, qr.L)
	if err != nil {
		return err
	}
	const quiet = 2
	n := code.Size
	black := func(x, y int) bool {
		if x < 0 || y < 0 || x >= n || y >= n {
			return false
		}
		return code.Black(x, y)
	}
	var b strings.Builder
	for y := -quiet; y < n+quiet; y += 2 {
		for x := -quiet; x < n+quiet; x++ {
			top, bot := black(x, y), black(x, y+1)
			// Light-on-dark terminals: print modules as spaces on a white field.
			switch {
			case top && bot:
				b.WriteString(" ")
			case top:
				b.WriteString("▄")
			case bot:
				b.WriteString("▀")
			default:
				b.WriteString("█")
			}
		}
		b.WriteString("\n")
	}
	_, err = io.WriteString(w, b.String())
	return err
}
