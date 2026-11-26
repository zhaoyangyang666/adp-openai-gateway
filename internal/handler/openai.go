package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/zhaoyangyang666/adp-openai-gateway/internal/adp"
)

// OpenAIHandler OpenAI协议处理器
type OpenAIHandler struct {
	client   *adp.Client
	registry *adp.ModelRegistry
}

// NewOpenAIHandler 创建处理器
func NewOpenAIHandler(client *adp.Client, registry *adp.ModelRegistry) *OpenAIHandler {
	return &OpenAIHandler{client: client, registry: registry}
}

// ChatRequest 聊天请求
type ChatRequest struct {
	Model    string        `json:"model"`
	Messages []adp.Message `json:"messages"`
	Stream   bool          `json:"stream"`
}

// GetModels 获取模型列表（adp-default + 模型白名单，供客户端下拉框选择）
func (h *OpenAIHandler) GetModels(c *gin.Context) {
	data := []gin.H{
		{
			"id":       "adp-default",
			"object":   "model",
			"created":  time.Now().Unix(),
			"owned_by": "tencent-adp",
		},
	}
	for _, m := range h.registry.List() {
		ownedBy := m.Developer
		if ownedBy == "" {
			ownedBy = m.Provider
		}
		if ownedBy == "" {
			ownedBy = "tencent-adp"
		}
		data = append(data, gin.H{
			"id":       m.DisplayName(),
			"object":   "model",
			"created":  time.Now().Unix(),
			"owned_by": ownedBy,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   data,
	})
}

// ChatCompletions 处理聊天完成请求
func (h *OpenAIHandler) ChatCompletions(c *gin.Context) {
	var req ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"message": "Invalid request body",
				"type":    "invalid_request_error",
				"code":    "invalid_request",
			},
		})
		return
	}

	if len(req.Messages) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"message": "messages is required and must be a non-empty array",
				"type":    "invalid_request_error",
				"code":    "invalid_messages",
			},
		})
		return
	}

	requestID := fmt.Sprintf("chatcmpl-%s", uuid.New().String())
	created := time.Now().Unix()
	model := req.Model
	if model == "" {
		model = "adp-default"
	}

	// 模型白名单校验：adp-default（空串）走应用默认模型；
	// 其余必须命中白名单（ModelId / ModelName / Name，大小写不敏感）
	chatModelName := ""
	if model != "adp-default" {
		m, ok := h.registry.Resolve(model)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{
					"message": fmt.Sprintf("模型「%s」不在可用白名单中，请通过 GET /v1/models 查询可用模型（当前来源：%s）", model, h.registry.Source()),
					"type":    "invalid_request_error",
					"code":    "model_not_found",
				},
			})
			return
		}
		chatModelName = m.ChatModelName()
		model = chatModelName // 回显实际生效的模型名
	}

	if req.Stream {
		h.handleStreamRequest(c, req.Messages, requestID, created, model, chatModelName)
	} else {
		h.handleNonStreamRequest(c, req.Messages, requestID, created, model, chatModelName)
	}
}

func (h *OpenAIHandler) handleNonStreamRequest(c *gin.Context, messages []adp.Message, requestID string, created int64, model string, chatModelName string) {
	result, err := h.client.Chat(messages, adp.ChatOptions{
		ModelName: chatModelName,
		Stream:    false,
	})

	if err != nil {
		log.Printf("[OpenAIHandler] 请求失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"message": err.Error(),
				"type":    "api_error",
				"code":    "internal_error",
			},
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":      requestID,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []gin.H{
			{
				"index": 0,
				"message": gin.H{
					"role":    "assistant",
					"content": result.Content,
				},
				"finish_reason": "stop",
			},
		},
		"usage": gin.H{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		},
	})
}

func (h *OpenAIHandler) handleStreamRequest(c *gin.Context, messages []adp.Message, requestID string, created int64, model string, chatModelName string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"message": "Streaming not supported",
				"type":    "api_error",
			},
		})
		return
	}

	done := make(chan struct{})
	errCh := make(chan error, 1)

	go func() {
		_, err := h.client.Chat(messages, adp.ChatOptions{
			ModelName: chatModelName,
			Stream:    true,
			OnChunk: func(chunk adp.Chunk) {
				var data []byte
				
				switch chunk.Type {
				case "content":
					data, _ = json.Marshal(gin.H{
						"id":      requestID,
						"object":  "chat.completion.chunk",
						"created": created,
						"model":   model,
						"choices": []gin.H{
							{
								"index":         0,
								"delta":         gin.H{"content": chunk.Content},
								"finish_reason": nil,
							},
						},
					})
					c.Writer.Write([]byte(fmt.Sprintf("data: %s\n\n", data)))
					flusher.Flush()

				case "done":
					data, _ = json.Marshal(gin.H{
						"id":      requestID,
						"object":  "chat.completion.chunk",
						"created": created,
						"model":   model,
						"choices": []gin.H{
							{
								"index":         0,
								"delta":         gin.H{},
								"finish_reason": "stop",
							},
						},
					})
					c.Writer.Write([]byte(fmt.Sprintf("data: %s\n\n", data)))
					c.Writer.Write([]byte("data: [DONE]\n\n"))
					flusher.Flush()
					close(done)
				}
			},
		})

		if err != nil {
			errCh <- err
		}
	}()

	select {
	case <-done:
		return
	case err := <-errCh:
		log.Printf("[OpenAIHandler] 流式请求失败: %v", err)
		// 错误已经无法发送到客户端，因为已经开始流式响应
		return
	case <-c.Request.Context().Done():
		return
	}
}
