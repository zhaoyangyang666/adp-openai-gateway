package handler

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/zhaoyangyang666/adp-openai-gateway/internal/keys"
)

const (
	sessionCookie  = "admin_session"
	sessionTimeout = 24 * time.Hour
)

// AdminHandler API Key 管理页面（管理员密码登录）
type AdminHandler struct {
	store    *keys.Store
	password string
	mu       sync.Mutex
	sessions map[string]time.Time // token -> 过期时间
}

// NewAdminHandler 创建管理端处理器
func NewAdminHandler(store *keys.Store, adminPassword string) *AdminHandler {
	return &AdminHandler{
		store:    store,
		password: adminPassword,
		sessions: map[string]time.Time{},
	}
}

// Register 注册路由
func (h *AdminHandler) Register(r *gin.Engine) {
	admin := r.Group("/admin")
	{
		admin.GET("", h.Page)
		admin.POST("/api/login", h.Login)
		admin.POST("/api/logout", h.Logout)
	}
	auth := admin.Group("/api", h.requireSession)
	{
		auth.GET("/keys", h.ListKeys)
		auth.POST("/keys", h.CreateKey)
		auth.PUT("/keys/:key", h.UpdateKey)
		auth.DELETE("/keys/:key", h.DeleteKey)
	}
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Page 管理页面（内嵌单文件 HTML）
//
//go:embed admin.html
var adminPageHTML string

func (h *AdminHandler) Page(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, adminPageHTML)
}

func (h *AdminHandler) Login(c *gin.Context) {
	if h.password == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "网关未配置 ADMIN_PASSWORD，管理页面不可用"})
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Password != h.password {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "密码错误"})
		return
	}
	token := randomToken()
	h.mu.Lock()
	h.sessions[token] = time.Now().Add(sessionTimeout)
	h.mu.Unlock()
	c.SetCookie(sessionCookie, token, int(sessionTimeout.Seconds()), "/admin", "", false, true)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *AdminHandler) Logout(c *gin.Context) {
	if token, err := c.Cookie(sessionCookie); err == nil {
		h.mu.Lock()
		delete(h.sessions, token)
		h.mu.Unlock()
	}
	c.SetCookie(sessionCookie, "", -1, "/admin", "", false, true)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *AdminHandler) requireSession(c *gin.Context) {
	if h.password == "" {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "网关未配置 ADMIN_PASSWORD，管理页面不可用"})
		return
	}
	token, err := c.Cookie(sessionCookie)
	if err != nil || token == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	h.mu.Lock()
	exp, ok := h.sessions[token]
	if ok && time.Now().After(exp) {
		delete(h.sessions, token)
		ok = false
	}
	h.mu.Unlock()
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "登录已过期"})
		return
	}
	c.Next()
}

func (h *AdminHandler) ListKeys(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"keys": h.store.List()})
}

func (h *AdminHandler) CreateKey(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	k, err := h.store.Add(req.Name, req.Note)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"key": k})
}

func (h *AdminHandler) UpdateKey(c *gin.Context) {
	key := c.Param("key")
	var req struct {
		Name    *string `json:"name"`
		Note    *string `json:"note"`
		Enabled *bool   `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	k, ok := h.store.Update(key, req.Name, req.Note, req.Enabled)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "key 不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"key": k})
}

func (h *AdminHandler) DeleteKey(c *gin.Context) {
	key := c.Param("key")
	if !h.store.Delete(key) {
		c.JSON(http.StatusNotFound, gin.H{"error": "key 不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
