package adp

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Client ADP v2 HTTP SSE 客户端（新版对话接口，支持 Claw 模式应用）
type Client struct {
	appKey     string
	secretId   string // 管理 API 凭证（模型白名单 DescribeModelList 用）
	secretKey  string
	apiURL     string
	httpClient *http.Client
}

// ChatResult 聊天结果
type ChatResult struct {
	Content   string
	RequestID string
}

// Chunk 流式数据块
type Chunk struct {
	Type    string // content, thought, done
	Content string
	IsEnd   bool
}

// Message OpenAI格式消息
type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string 或 []ContentPart
}

// ContentPart 多模态内容部分
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL 图片URL
type ImageURL struct {
	URL string `json:"url"`
}

// ChatOptions 聊天选项
type ChatOptions struct {
	SessionID      string
	ModelName      string // 动态指定模型（ADP v2 ModelName 参数，空则用应用默认模型）
	Stream         bool
	OnChunk        func(chunk Chunk)
	IncludeThought bool
	Timeout        time.Duration
}

// NewClient 创建ADP客户端（secretId/secretKey 用于管理 API：模型白名单拉取；对话接口仅使用 AppKey）
func NewClient(secretId, secretKey, botAppKey string) *Client {
	return &Client{
		appKey:     botAppKey,
		secretId:   secretId,
		secretKey:  secretKey,
		apiURL:     "https://wss.lke.cloud.tencent.com/adp/v2/chat",
		httpClient: &http.Client{},
	}
}

// ========== v2 接口请求/响应结构 ==========

type v2Content struct {
	Type string `json:"Type"`
	Text string `json:"Text,omitempty"`
}

type v2ChatRequest struct {
	RequestId      string      `json:"RequestId"`
	ConversationId string      `json:"ConversationId"`
	AppKey         string      `json:"AppKey"`
	UserId         string      `json:"UserId"`
	VisitorId      string      `json:"VisitorId"`
	Contents       []v2Content `json:"Contents"`
	Stream         string      `json:"Stream"`
	Incremental    bool        `json:"Incremental"`
	ModelName      string      `json:"ModelName,omitempty"` // 动态指定模型（官方对话接口可选参数）
}

type v2TextDelta struct {
	Type      string `json:"Type"`
	MessageId string `json:"MessageId"`
	Text      string `json:"Text"`
}

type v2MessageContent struct {
	Type string `json:"Type"`
	Text string `json:"Text"`
}

type v2Message struct {
	Type      string            `json:"Type"`
	MessageId string            `json:"MessageId"`
	Status    string            `json:"Status"`
	Contents  []v2MessageContent `json:"Contents"`
}

type v2MessageAdded struct {
	Type    string    `json:"Type"`
	Message v2Message `json:"Message"`
}

type v2MessageDone struct {
	Type      string    `json:"Type"`
	MessageId string    `json:"MessageId"`
	Message   v2Message `json:"Message"`
}

type v2ErrorEvent struct {
	Error struct {
		Code    int    `json:"Code"`
		Message string `json:"Message"`
	} `json:"Error"`
}

type v2StatInfo struct {
	InputTokens  int    `json:"InputTokens"`
	OutputTokens int    `json:"OutputTokens"`
	TotalTokens  int    `json:"TotalTokens"`
	TotalCost    string `json:"TotalCost"`
}

type v2ResponseCompleted struct {
	Response struct {
		StatInfo v2StatInfo `json:"StatInfo"`
	} `json:"Response"`
}

// validConvID 校验会话ID是否符合v2接口要求 ^[a-zA-Z0-9_-]{32,64}$
func validConvID(s string) bool {
	if len(s) < 32 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// messageText 提取单条OpenAI消息的文本内容
func messageText(m Message) string {
	switch v := m.Content.(type) {
	case string:
		return v
	case []interface{}:
		var sb strings.Builder
		for _, part := range v {
			if p, ok := part.(map[string]interface{}); ok {
				if p["type"] == "text" {
					if text, ok := p["text"].(string); ok {
						sb.WriteString(text)
					}
				}
			}
		}
		return sb.String()
	}
	return ""
}

// extractText 从OpenAI消息中提取最后一条消息的文本内容
func extractText(messages []Message) string {
	return messageText(messages[len(messages)-1])
}

// deriveConversationID 从第一条用户消息派生稳定的会话ID。
// 同一通对话中，客户端每次都会携带相同的消息前缀，第一条用户消息不变，
// 因此哈希值稳定，ADP 侧即可复用同一会话、原生维护多轮上下文。
func deriveConversationID(messages []Message) string {
	for _, m := range messages {
		if m.Role == "user" {
			text := messageText(m)
			if text != "" {
				sum := sha256.Sum256([]byte(text))
				return hex.EncodeToString(sum[:])[:32] // 32位十六进制，符合v2接口 ^[a-zA-Z0-9_-]{32,64}$
			}
		}
	}
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}

// Chat 发送聊天请求（走 ADP v2 HTTP SSE 接口）
func (c *Client) Chat(messages []Message, opts ChatOptions) (*ChatResult, error) {
	content := extractText(messages)
	if content == "" {
		return nil, fmt.Errorf("消息内容为空")
	}

	conversationID := opts.SessionID
	if !validConvID(conversationID) {
		conversationID = deriveConversationID(messages)
	}
	requestID := strings.ReplaceAll(uuid.New().String(), "-", "")

	streamStr := "disable"
	incremental := false
	if opts.Stream {
		streamStr = "enable"
		incremental = true
	}

	body := v2ChatRequest{
		RequestId:      requestID,
		ConversationId: conversationID,
		AppKey:         c.appKey,
		UserId:         "adp-openai-gateway",
		VisitorId:      "adp-openai-gateway",
		Contents:       []v2Content{{Type: "text", Text: content}},
		Stream:         streamStr,
		Incremental:    incremental,
		ModelName:      opts.ModelName,
	}

	bodyBytes, _ := json.Marshal(body)

	log.Println("[ADPClient] ========== 发送聊天请求 (v2 HTTP SSE) ==========")
	log.Printf("[ADPClient] request_id: %s", requestID)
	log.Printf("[ADPClient] conversation_id: %s", conversationID)
	if opts.ModelName != "" {
		log.Printf("[ADPClient] 动态模型: %s", opts.ModelName)
	}
	log.Printf("[ADPClient] 消息内容: %s", truncate(content, 100))
	log.Printf("[ADPClient] Stream模式: %v", opts.Stream)

	httpReq, err := http.NewRequest("POST", c.apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("构建请求失败: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("请求ADP失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ADP返回HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 300))
	}

	// 解析 SSE 流
	msgTypes := map[string]string{} // messageId -> message type (reply/thought/...)
	var deltaContent strings.Builder
	var replyText string

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 300 * time.Second
	}
	type result struct {
		text string
		err  error
	}
	doneCh := make(chan result, 1)

	go func() {
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				break
			}

			var generic struct {
				Type string `json:"Type"`
			}
			if err := json.Unmarshal([]byte(data), &generic); err != nil {
				continue
			}

			switch generic.Type {
			case "message.added":
				var ev v2MessageAdded
				if json.Unmarshal([]byte(data), &ev) == nil && ev.Message.MessageId != "" {
					msgTypes[ev.Message.MessageId] = ev.Message.Type
				}

			case "text.delta":
				var ev v2TextDelta
				if json.Unmarshal([]byte(data), &ev) != nil {
					continue
				}
				if msgTypes[ev.MessageId] != "reply" || ev.Text == "" {
					continue
				}
				deltaContent.WriteString(ev.Text)
				if opts.Stream && opts.OnChunk != nil {
					log.Printf("[ADPClient] 发送增量内容: %s...", truncate(ev.Text, 50))
					opts.OnChunk(Chunk{Type: "content", Content: ev.Text})
				}

			case "message.done":
				var ev v2MessageDone
				if json.Unmarshal([]byte(data), &ev) != nil {
					continue
				}
				if ev.Message.Type == "reply" {
					var sb strings.Builder
					for _, ct := range ev.Message.Contents {
						if ct.Type == "text" {
							sb.WriteString(ct.Text)
						}
					}
					if sb.Len() > 0 {
						replyText = sb.String()
					}
				}

			case "response.completed":
				var ev v2ResponseCompleted
				if json.Unmarshal([]byte(data), &ev) == nil {
					log.Printf("[ADPClient] Token用量: 输入=%d 输出=%d 总计=%d 费用=%s",
						ev.Response.StatInfo.InputTokens, ev.Response.StatInfo.OutputTokens,
						ev.Response.StatInfo.TotalTokens, ev.Response.StatInfo.TotalCost)
				}

			case "error":
				var ev v2ErrorEvent
				if json.Unmarshal([]byte(data), &ev) == nil {
					doneCh <- result{err: fmt.Errorf("ADP错误 %d: %s", ev.Error.Code, ev.Error.Message)}
					return
				}
			}
		}

		doneCh <- result{text: replyText}
	}()

	select {
	case <-time.After(timeout):
		return nil, fmt.Errorf("请求超时")
	case r := <-doneCh:
		if r.err != nil {
			return nil, r.err
		}
		finalText := r.text
		if finalText == "" {
			finalText = deltaContent.String()
		}
		if finalText == "" {
			return nil, fmt.Errorf("ADP未返回内容")
		}
		log.Printf("[ADPClient] 请求完成，内容长度: %d", len(finalText))
		if opts.Stream && opts.OnChunk != nil {
			opts.OnChunk(Chunk{Type: "done"})
		}
		return &ChatResult{
			Content:   finalText,
			RequestID: requestID,
		}, nil
	}
}

// Disconnect 断开连接（v2 HTTP 接口为短连接，无需处理）
func (c *Client) Disconnect() {}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
