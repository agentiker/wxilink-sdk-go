# wxilink-sdk-go

微信 [iLink Bot API](https://ilinkai.weixin.qq.com) 的 Go SDK。

> Fork 自 [openilink/openilink-sdk-go](https://github.com/openilink/openilink-sdk-go) v0.6.0（MIT），基线 tag：`upstream-v0.6.0`。
> iLink 为腾讯非公开协议，本项目与腾讯无关联，仅供学习研究。

```
go get github.com/agentiker/wxilink-sdk-go
```

## 特性

- 扫码登录，支持扫码/过期回调
- 长轮询消息监听，自动重试与退避，动态超时
- 主动推送（自动缓存 contextToken，缺失时照发）
- 发送图片、视频、文件，MIME 自动路由
- CDN 加密上传/下载（AES-128-ECB）
- 语音消息解码（可插拔 SILK 解码器 + WAV 封装）
- 输入状态指示器、Bot 配置
- Functional Options 配置模式
- `HTTPDoer` 接口，方便自定义传输层和测试
- 结构化错误类型（`APIError`、`HTTPError`），正确识别 HTTP 200 里的 `ret`/`errcode` 错误
- 零外部依赖，仅使用标准库

## 快速开始

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	ilink "github.com/agentiker/wxilink-sdk-go"
)

func main() {
	client := ilink.NewClient("")

	// 扫码登录
	result, err := client.LoginWithQR(context.Background(), &ilink.LoginCallbacks{
		OnQRCode:  func(img string) { fmt.Printf("请扫码:\n%s\n", img) },
		OnScanned: func() { fmt.Println("已扫码，请在微信上确认...") },
	})
	if err != nil || !result.Connected {
		log.Fatal("登录失败")
	}
	fmt.Printf("已连接 BotID=%s\n", result.BotID)

	// 监听消息 & 自动回复
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	client.Monitor(ctx, func(msg ilink.WeixinMessage) {
		text := ilink.ExtractText(&msg)
		if text != "" {
			client.Push(ctx, msg.FromUserID, "收到: "+text)
		}
	}, &ilink.MonitorOptions{
		OnBufUpdate: func(buf string) {
			os.WriteFile("sync_buf.dat", []byte(buf), 0600)
		},
	})
}
```

## API

### 创建客户端

```go
// 默认配置
client := ilink.NewClient(token)

// 自定义配置
client := ilink.NewClient(token,
    ilink.WithBaseURL("https://custom.endpoint.com"),
    ilink.WithCDNBaseURL("https://custom.cdn.com/c2c"),
    ilink.WithHTTPClient(myHTTPClient),
    ilink.WithBotType("3"),
    ilink.WithVersion("1.0.2"),
    ilink.WithRouteTag("my-route-tag"),
    ilink.WithSILKDecoder(mySILKDecoder),
    ilink.WithAppID("bot"),                    // iLink-App-Id 请求头，默认同上游
    ilink.WithClientIDPrefix("openclaw-weixin"), // client_id 前缀，默认同上游
)
```

### 扫码登录

```go
result, err := client.LoginWithQR(ctx, &ilink.LoginCallbacks{
    OnQRCode:  func(imgContent string) { /* 展示二维码 */ },
    OnScanned: func() { /* 用户已扫码 */ },
    OnExpired: func(attempt, max int) { /* 二维码过期，正在刷新 */ },
})
// result.Connected, result.BotID, result.BotToken, result.UserID
```

登录成功后，客户端的 Token 和 BaseURL 会自动更新。

### 接收消息

```go
err := client.Monitor(ctx, func(msg ilink.WeixinMessage) {
    text := ilink.ExtractText(&msg) // 支持引用消息和语音转文字
    // msg.FromUserID, msg.ContextToken, msg.ItemList
}, &ilink.MonitorOptions{
    InitialBuf:       savedBuf,          // 从上次位置恢复
    OnBufUpdate:      func(buf string) { /* 持久化游标 */ },
    OnError:          func(err error) { /* 记录错误 */ },
    OnSessionExpired: func() { /* 重新登录 */ },
})
```

Monitor 会自动缓存每个用户的 contextToken，供 `Push` 使用。服务端返回的 `longpolling_timeout_ms` 会被自动采纳。

### 发送文本

```go
// 回复消息（contextToken 可选，回复时建议回传入站消息的 ContextToken）
client.SendText(ctx, userID, "你好", contextToken)

// 主动推送（有缓存的 contextToken 就带上，没有则不带）
client.Push(ctx, userID, "这是一条定时通知")
```

> **发送限制（实测，见下文「iLink 协议实测行为」）**：用户每给 Bot 发一条消息，Bot 可以向该用户发 **10 条**；
> 用完后返回 `ret=-2 prepare failed`（`apiErr.IsNoSession()`），用户再发一条即恢复。
> `contextToken` 不影响这一点：不传、传伪造值都照常发送。

### 发送媒体

```go
// 高级接口：自动识别 MIME 类型 → 上传 → 发送
data, _ := os.ReadFile("photo.jpg")
client.SendMediaFile(ctx, userID, contextToken, data, "photo.jpg", "看看这张图")

// 分步操作：上传 → 发送
uploaded, _ := client.UploadFile(ctx, data, userID, ilink.MediaImage)
client.SendImage(ctx, userID, contextToken, uploaded)
client.SendVideo(ctx, userID, contextToken, uploaded)
client.SendFileAttachment(ctx, userID, contextToken, "report.pdf", uploaded)
```

### 下载媒体

```go
for _, item := range msg.ItemList {
    switch item.Type {
    case ilink.ItemImage:
        data, _ := client.DownloadMedia(ctx, item.ImageItem.Media)

    case ilink.ItemVoice:
        // 需要配置 WithSILKDecoder
        wav, _ := client.DownloadVoice(ctx, item.VoiceItem)
    }
}
```

### 语音解码

SDK 通过可插拔的 `SILKDecoder` 支持语音消息解码，保持零外部依赖。以 [youthlin/silk](https://github.com/youthlin/silk) 为例：

```go
import "github.com/youthlin/silk"

client := ilink.NewClient(token, ilink.WithSILKDecoder(
    func(data []byte, sampleRate int) ([]byte, error) {
        return silk.Decode(bytes.NewReader(data),
            silk.WithSampleRate(sampleRate))
    },
))

wav, err := client.DownloadVoice(ctx, voiceItem) // 返回 WAV 文件字节
```

也可以单独使用 WAV 封装：

```go
wav := ilink.BuildWAV(pcmData, 24000, 1, 16) // sampleRate, channels, bitsPerSample
```

### 其他

```go
// 输入状态
client.SendTyping(ctx, userID, ticket, ilink.Typing)
client.SendTyping(ctx, userID, ticket, ilink.CancelTyping)

// Bot 配置（含 typing_ticket）
config, _ := client.GetConfig(ctx, userID, contextToken)

// 工具函数
ilink.IsMediaItem(&item)                  // 是否为媒体类型
ilink.MIMEFromFilename("photo.jpg")       // "image/jpeg"
ilink.ExtensionFromMIME("video/mp4")      // ".mp4"
ilink.IsImageMIME("image/png")            // true
ilink.IsVideoMIME("video/mp4")            // true
```

## 错误处理

iLink 几乎所有失败都返回 **HTTP 200**，错误在 body 的 `ret` / `errcode` 里。SDK 会把它们统一转成 `*APIError`
（上游 v0.6.0 的 `SendMessage` / `SendTyping` / `GetConfig` 会静默吞掉这类错误，本 fork 已修复）。

| 返回 | 含义 | 判断 |
|---|---|---|
| `ret=-2 prepare failed` | 目标用户与 Bot 无会话（未互动过 / 会话失效） | `IsNoSession()` |
| `ret=-3 invalid arguments` | 参数错误（如 to_user_id 无效） | `IsInvalidArgument()` |
| `errcode=-14 session timeout` | bot_token 失效，需重新扫码 | `IsSessionExpired()` |

```go
import "errors"

var apiErr *ilink.APIError
if errors.As(err, &apiErr) {
    if apiErr.IsNoSession() {
        // 该用户尚未给 Bot 发过消息，无法主动推送
    }
    if apiErr.IsSessionExpired() {
        // 需要重新登录
    }
    fmt.Println(apiErr.ErrCode, apiErr.ErrMsg)
}

var httpErr *ilink.HTTPError
if errors.As(err, &httpErr) {
    fmt.Println(httpErr.StatusCode)
}
```

## 常量

```go
// 媒体类型（用于 UploadFile）
ilink.MediaImage   // 1
ilink.MediaVideo   // 2
ilink.MediaFile    // 3
ilink.MediaVoice   // 4

// 消息类型
ilink.MsgTypeUser  // 1 - 用户消息
ilink.MsgTypeBot   // 2 - Bot 消息

// 消息项类型
ilink.ItemText     // 1
ilink.ItemImage    // 2
ilink.ItemVoice    // 3
ilink.ItemFile     // 4
ilink.ItemVideo    // 5

// 消息状态
ilink.StateNew        // 0
ilink.StateGenerating // 1
ilink.StateFinish     // 2
```

## 轻量推送：`push` 包与 `wxpush`

只想「扫一次码，之后随时给自己发通知」，不需要跑完整 Bot：

```bash
go install github.com/agentiker/wxilink-sdk-go/cmd/wxpush@latest

wxpush login                       # 终端显示二维码，用微信扫码；凭证存到 <UserConfigDir>/wxilink/credentials.json (0600)
                                   # 然后在微信里给 Bot 发任意一条消息
wxpush send "构建完成 ✅"           # 默认发给扫码的微信号
printf '多行\n内容' | wxpush send -  # 从 stdin 读
wxpush file -caption 日报 a.pdf    # 图片/视频/文件按扩展名自动识别
wxpush check                       # 静默检查 token 是否有效（不发消息，不能预测配额）
wxpush whoami
wxpush probe -interval 3s          # 连续发带编号的消息直到被拒，实测配额
wxpush quota                       # 查看剩余配额（-reset 在对方回复后重置）
```

退出码：`0` 成功，`1` 错误，`2` 参数错误，`3` 无会话/配额用完（需在微信里给 Bot 发一条消息）。
凭证路径优先级：`-creds` > `$WXILINK_CREDS` > 默认路径。

Go 代码里使用：

```go
import "github.com/agentiker/wxilink-sdk-go/push"

creds, err := push.Login(ctx, &push.LoginOptions{OnQRCode: func(url string) { /* 渲染二维码 */ }})
creds.Save(path)

p := push.New(creds)          // 或 push.Load(path)
p.Text(ctx, "", "hello")      // "" = 扫码的微信号
p.File(ctx, "", "a.png", data, "说明")
```

`ilink` 核心包保持零依赖；`rsc.io/qr` 仅被 `cmd/wxpush` 引用。

### 发送配额与提醒（`quota` 包）

接收方并不知道「每发 1 条消息，Bot 只能回 10 条」。为避免第 11 条起悄悄发不出去，
约定**第 10 条（最后一条）附带提醒**：

> 构建完成 ✅
>
> 💬 回复任意消息，可继续接收后续通知

- `quota.Counter`：按接收人计数（`Inbound` 重置、`Sent` 累加、`Refused` 以服务端 `ret=-2` 为准），可 JSON 持久化
- `quota.Nudge(pending)` / `quota.WithNudge(text, pending)` / `quota.Cost(hasMedia, hasCaption)`
- `push.Pusher.UseQuota(c)`：自动在最后一条附加提醒；文件遇到只剩 1 条时改发提醒并返回 `push.ErrQuotaDeferred`；
  看不到对方回复，因此「计数已满但发送成功」即推断对方已回复、计数重置
- `wxpush` 默认启用，计数存在 `<creds>.quota.json`；`wxpush quota` 查看，`wxpush quota -reset` 手动重置，`-no-quota` 关闭

完整的「超额排队、对方回复后自动补发」需要能收到消息的长连接进程，见 [openilink-hub](https://github.com/agentiker/openilink-hub)。

## iLink 协议实测行为

iLink 没有公开文档。以下是用真实服务端测出的行为，由 `live_test.go` 固化（`-tags live`）：

| 行为 | 结论 |
|---|---|
| 错误返回方式 | 几乎总是 **HTTP 200**，错误在 body 的 `ret`/`errcode` |
| 用户从未给 Bot 发过消息 | `sendmessage` → `ret=-2 prepare failed`，`getconfig` → `ret=-4` |
| `context_token` | **不必需、不校验**：不传 / 传伪造值 / 传真实值，结果相同。与上游插件一致（缺 token 仅告警照发） |
| 发送配额 | 用户每发 1 条消息，Bot 可向其发 **10 条**（文本、图片、文件、媒体的 caption 各算 1 条）。第 11 条 → `ret=-2`。与时间无关：31 秒内即可用完 |
| 配额恢复 | 用户再发 1 条消息即**重置为 10 条**，剩余的不累加（实测：用 3 条后用户连发 2 条，仍只能再发 7 条） |
| `getconfig` | 只反映「是否互动过」，配额用完后仍返回成功，**不能**用来预测能否发送 |
| 未知接收人 | `ret=-3 invalid arguments` |
| bot_token 无效 | `errcode=-14 session timeout` |
| 必需请求头 | 缺少 `AuthorizationType` / `iLink-App-Id` 等 → `errcode=-14` |

**未测**：没有新的用户消息时配额多久失效（时间窗口）；同一 Bot 面对多个用户时是否分别计数。

运行 live 测试（会给你发 6 条消息，请先在微信里给 Bot 发一条）：

```bash
WXILINK_LIVE_CREDS=~/Library/Application\ Support/wxilink/credentials.json go test -tags live -run Live -v .
```

## 许可证

MIT
