package main

import (
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	"github.com/zhaoyangyang666/adp-openai-gateway/internal/adp"
	"github.com/zhaoyangyang666/adp-openai-gateway/internal/handler"
	"github.com/zhaoyangyang666/adp-openai-gateway/internal/keys"
)

// authMiddleware API Key 校验：Authorization: Bearer <key>（或裸 key）。
// key 由管理页面分配，存储在 gateway-keys.json。
func authMiddleware(store *keys.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := strings.TrimSpace(c.GetHeader("Authorization"))
		key = strings.TrimPrefix(key, "Bearer ")
		key = strings.TrimSpace(key)
		if key == "" {
			key = strings.TrimSpace(c.GetHeader("X-Api-Key"))
		}
		if !store.Validate(key) {
			log.Printf("[Gateway] 鉴权失败，来源: %s", c.ClientIP())
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{
					"message": "Invalid API key. 请使用网关分配的 API Key（Authorization: Bearer <key>）",
					"type":    "invalid_request_error",
					"code":    "invalid_api_key",
				},
			})
			return
		}
		c.Set("api_key", key)
		c.Next()
	}
}

func main() {
	// 加载配置
	if err := godotenv.Load(".env"); err != nil {
		log.Println("[Gateway] 未找到.env，使用环境变量")
	}

	// 验证配置
	secretId := os.Getenv("SECRET_ID")
	secretKey := os.Getenv("SECRET_KEY")
	botAppKey := os.Getenv("ADP_BOT_APP_KEY")

	if secretId == "" || secretKey == "" || botAppKey == "" {
		log.Fatal("[Gateway] 错误: 缺少必要环境变量 SECRET_ID, SECRET_KEY, ADP_BOT_APP_KEY")
	}

	// 初始化客户端
	client := adp.NewClient(secretId, secretKey, botAppKey)

	// 模型白名单：启动时拉取 ADP Claw 可用模型（ModelScene=18），每小时刷新；
	// 未配置 ADP_SPACE_ID 或拉取失败时使用内置静态清单兜底
	registry := adp.NewModelRegistry()
	spaceId := os.Getenv("ADP_SPACE_ID")
	region := os.Getenv("ADP_REGION")
	if region == "" {
		region = "ap-guangzhou"
	}
	if spaceId == "" {
		log.Println("[Gateway] 未配置 ADP_SPACE_ID，模型白名单使用内置静态清单")
	} else if err := registry.Refresh(secretId, secretKey, spaceId, region); err != nil {
		log.Printf("[Gateway] 启动拉取模型清单失败（暂用内置静态清单）: %v", err)
	}
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := registry.Refresh(secretId, secretKey, spaceId, region); err != nil {
				log.Printf("[Gateway] 模型清单刷新失败（沿用上一次清单）: %v", err)
			}
		}
	}()

	openaiHandler := handler.NewOpenAIHandler(client, registry)

	// 网关 API Key：存储在 gateway-keys.json，由管理页面（/admin）分配管理。
	// 兼容迁移：首次运行且存储为空时，把 .env GATEWAY_KEYS 里的 key 一次性导入
	keyStorePath := os.Getenv("GATEWAY_KEYS_FILE")
	if keyStorePath == "" {
		keyStorePath = "gateway-keys.json"
	}
	keyStore, err := keys.NewStore(keyStorePath)
	if err != nil {
		log.Fatalf("[Gateway] key 存储初始化失败: %v", err)
	}
	keyStore.ImportEnvKeys(os.Getenv("GATEWAY_KEYS"))
	if strings.TrimSpace(os.Getenv("GATEWAY_KEYS")) != "" {
		log.Println("[Gateway] 提示: GATEWAY_KEYS 已废弃，请在管理页面维护 key（本次已自动迁移）")
	}

	adminPassword := os.Getenv("ADMIN_PASSWORD")
	if adminPassword == "" {
		log.Println("[Gateway] 警告: 未配置 ADMIN_PASSWORD，管理页面 /admin 不可用（无法分配 key）")
	} else {
		log.Printf("[Gateway] 管理页面: http://<host:port>/admin（key 存储: %s）", keyStore.FilePath())
	}

	// 设置Gin
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		return ""
	}))

	// 管理页面路由
	handler.NewAdminHandler(keyStore, adminPassword).Register(r)

	// 路由
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status":       "ok",
			"service":      "adp-openai-gateway-go",
			"model_source": registry.Source(),
		})
	})

	v1 := r.Group("/v1", authMiddleware(keyStore))
	v1.GET("/models", openaiHandler.GetModels)
	v1.POST("/chat/completions", openaiHandler.ChatCompletions)

	// 获取端口
	port := os.Getenv("PORT")
	if port == "" {
		port = "3100"
	}
	host := os.Getenv("HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	addr := host + ":" + port

	log.Printf("[Gateway] ADP-OpenAI Gateway (Go) 运行在 %s", addr)
	log.Printf("[Gateway] API端点: http://%s/v1/chat/completions", addr)

	// 优雅关闭
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		<-sigCh
		log.Println("[Gateway] 收到关闭信号，正在关闭...")
		client.Disconnect()
		os.Exit(0)
	}()

	if err := r.Run(addr); err != nil {
		log.Fatalf("[Gateway] 启动失败: %v", err)
	}
}
