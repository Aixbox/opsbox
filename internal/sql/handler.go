package sql

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"opsbox/internal/api/response"
	"opsbox/internal/platform/opscheck"
	"opsbox/internal/platform/sqlx"
)

// localUserID 是本地单用户模式下的固定用户 ID（opsbox 无账号体系，审计里的操作人都记为它）。
const localUserID int64 = 1

// Handler 挂载 /api/v1/sql/* 路由。
type Handler struct {
	service  *Service
	upgrader websocket.Upgrader
}

// NewHandler 创建 handler。本地应用不做 Origin 白名单校验：服务只监听 127.0.0.1，
// WebSocket 由一次性 ticket 鉴权，Origin 校验没有实际增益。
func NewHandler(service *Service) *Handler {
	return &Handler{
		service: service,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  8 * 1024,
			WriteBufferSize: 8 * 1024,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
	}
}

// RegisterRoutes 挂载全部路由。本地单用户：无登录、无权限码，中间件只剩 CORS。
func (h *Handler) RegisterRoutes(group *gin.RouterGroup) {
	g := group.Group("/sql")
	g.GET("/connections", h.listConnections)
	g.POST("/connections", h.createConnection)
	g.GET("/connections/:id", h.getConnection)
	g.PUT("/connections/:id", h.updateConnection)
	g.DELETE("/connections/:id", h.deleteConnection)
	g.POST("/connections/:id/test", h.testConnection)
	g.POST("/connections/:id/query", h.query)
	g.POST("/connections/:id/query/check", h.checkQuery)
	// 控制台会话：本地模式会话即授权
	g.POST("/connections/:id/sessions", h.openSession)
	g.GET("/connections/:id/sessions", h.listConnectionSessions)
	g.GET("/sessions", h.listSessions)
	g.POST("/connections/:id/sessions/:sid/ticket", h.sessionTicket)
	g.POST("/connections/:id/sessions/:sid/rename", h.renameSession)
	g.DELETE("/connections/:id/sessions/:sid", h.closeSession)
	g.GET("/connections/:id/tables", h.listTables)
	g.GET("/connections/:id/tables/:name/schema", h.tableSchema)
	g.GET("/queries/:id", h.getQuery)
	g.GET("/pending-queries", h.listPending)
	g.POST("/queries/:id/approve", h.approve)
	g.POST("/queries/:id/reject", h.reject)
	g.GET("/query-logs", h.listLogs)
	g.GET("/settings", h.getSettings)
	g.PUT("/settings", h.updateSettings)
	// WebSocket 端点：浏览器握手带不了 Authorization，凭一次性 ticket 鉴权。
	// 原项目因「公开路由」而单独注册，本地单用户无鉴权差异，直接并入。
	g.GET("/connections/:id/sessions/:sid/ws", h.consoleWS)
}

// consoleWS 把控制台会话的事件流转给浏览器；客户端发来的 {"sql":"..."} 直接在面板里执行。
func (h *Handler) consoleWS(c *gin.Context) {
	session, err := h.service.AttachSession(c.Query("ticket"))
	if err != nil {
		fail(c, err)
		return
	}
	defer session.Detach()
	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	// replay=true：新接入 / 断线重连的面板先补回历史事件，不至于空屏
	subscriber := session.NewSubscriber(true)
	defer subscriber.Close()

	done := make(chan struct{})
	// stop 在浏览器断开后叫停推送协程：订阅通道不会关闭，没有它推送协程会一直挂在 select 上，
	// 下面的 <-done 永远等不到——handler 不返回，deferred 的 Detach 也不执行，会话就永远不会被闲置回收。
	stop := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case event := <-subscriber.C():
				_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
				if err := conn.WriteJSON(event); err != nil {
					return
				}
			case <-session.Done():
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "会话已结束"))
				return
			case <-stop:
				return
			}
		}
	}()

	// 面板里手写的 SQL 走这条路径执行（来源标记为 web，与 CLI 操作区分展示）。
	// 用脱离请求的上下文：WebSocket 是长连接，把执行挂在请求上下文上，
	// 请求一旦超时/取消，后续每条语句都会在 resolve 处就失败——面板将永远收不到回显。
	conn.SetReadLimit(256 * 1024)
	for {
		var message struct {
			SQL string `json:"sql"`
			Tx  bool   `json:"tx"`
		}
		if err := conn.ReadJSON(&message); err != nil {
			break
		}
		if strings.TrimSpace(message.SQL) == "" {
			continue
		}
		go func(statement string, tx bool) {
			defer func() {
				if recovered := recover(); recovered != nil {
					_ = recovered
				}
			}()
			execCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 315*time.Second)
			defer cancel()
			_, _ = h.service.Query(execCtx, session.UserID, session.ConnectionID, QueryRequest{SQL: statement, Tx: tx, Source: "web"})
		}(message.SQL, message.Tx)
	}
	// 浏览器断开：只叫停推送并 Detach（见 defer），不关会话——会话是持久的，重新打开面板还能接回
	close(stop)
	<-done
}

// ---- 控制台会话 ----

func (h *Handler) openSession(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	info, err := h.service.OpenSession(c.Request.Context(), currentUser(c), id)
	if err != nil {
		fail(c, err)
		return
	}
	response.Created(c, "/api/v1/sql/connections/"+strconv.FormatInt(id, 10)+"/sessions", 0, info)
}

func (h *Handler) listConnectionSessions(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	items, err := h.service.ListSessions(c.Request.Context(), currentUser(c), id)
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", gin.H{"items": items})
}

func (h *Handler) listSessions(c *gin.Context) {
	items, err := h.service.ListSessions(c.Request.Context(), currentUser(c), 0)
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", gin.H{"items": items})
}

func (h *Handler) sessionTicket(c *gin.Context) {
	ticket, err := h.service.IssueTicket(currentUser(c), c.Param("sid"))
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", gin.H{"ticket": ticket})
}

func (h *Handler) closeSession(c *gin.Context) {
	if err := h.service.CloseSession(currentUser(c), c.Param("sid")); err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", nil)
}

// renameSession 给会话起名（标签页上显示）；空名字恢复默认的「会话 #N」。
func (h *Handler) renameSession(c *gin.Context) {
	var input struct {
		Name string `json:"name"`
	}
	if !bind(c, &input) {
		return
	}
	name, err := h.service.RenameSession(currentUser(c), c.Param("sid"), input.Name)
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", gin.H{"name": name})
}

// requestSource 区分这次操作来自 CLI 还是浏览器面板，供控制台面板做展示区分。
// 本地单用户模式没有 CLI 令牌，所有请求都来自桌面应用内的 Web 面板。
func requestSource(c *gin.Context) string {
	return "web"
}

// ---- helpers ----

// currentUser 本地单用户模式下固定返回 localUserID。
func currentUser(c *gin.Context) int64 {
	return localUserID
}

func parseID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id < 1 {
		response.Error(c, http.StatusBadRequest, "INVALID_REQUEST", "记录 ID 无效", nil)
		return 0, false
	}
	return id, true
}

func bind(c *gin.Context, input any) bool {
	if err := c.ShouldBindJSON(input); err != nil {
		response.Error(c, http.StatusBadRequest, "INVALID_REQUEST", "请求 JSON 无效", nil)
		return false
	}
	return true
}

func fail(c *gin.Context, err error) {
	status, code, message := http.StatusInternalServerError, "INTERNAL_ERROR", "处理失败，请稍后重试"
	switch {
	case errors.Is(err, ErrNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "记录不存在"
	case errors.Is(err, ErrInvalid):
		status, code, message = http.StatusUnprocessableEntity, "INVALID_SQL_REQUEST", err.Error()
	case errors.Is(err, ErrDisabled):
		status, code, message = http.StatusConflict, "SQL_CONNECTION_DISABLED", err.Error()
	case errors.Is(err, ErrNoOpenSession):
		status, code = http.StatusConflict, "SQL_NO_OPEN_SESSION"
		message = "该连接没有已打开的控制台会话。请先在 opsbox 窗口「数据库 → 控制台」打开该连接的控制台，再执行查询（语句与结果会显示在那个面板里）。"
	case errors.Is(err, ErrConflict), errors.Is(err, ErrForbidden):
		status, code, message = http.StatusConflict, "CONFLICT", err.Error()
	case errors.Is(err, sqlx.ErrTimeout):
		status, code, message = http.StatusGatewayTimeout, "SQL_TIMEOUT", err.Error()
	case errors.As(err, new(*opscheck.RequiredError)):
		// 需审批写操作缺少预检令牌：409 + 完整预检结果（含令牌），CLI 据此走「先确认再重提」协议
		var required *opscheck.RequiredError
		_ = errors.As(err, &required)
		response.JSON(c, http.StatusConflict, "SQL_CHECK_REQUIRED", required.Check.Guidance, required.Check)
		return
	default:
		// 连接失败 / 驱动报错等远端错误：把原因带给调用方（CLI 需要看到）
		text := err.Error()
		if strings.Contains(text, "connection refused") || strings.Contains(text, "dial") || strings.Contains(text, "timeout") || strings.Contains(text, "不存在") || strings.Contains(text, "连接") {
			status, code, message = http.StatusBadGateway, "SQL_UPSTREAM_ERROR", text
		}
	}
	response.Error(c, status, code, message, nil)
}

// extendDeadline 放宽本次请求的网络死线（服务器默认 30s 写超时会打断长查询）。
func extendDeadline(c *gin.Context, budget time.Duration) {
	controller := http.NewResponseController(c.Writer)
	_ = controller.SetReadDeadline(time.Now().Add(budget))
	_ = controller.SetWriteDeadline(time.Now().Add(budget + 10*time.Second))
}

// detachedCtx 脱离 QueryTimeout 中间件的 2s 全局死线，自管超时。
func detachedCtx(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(context.Background()), timeout)
}

// ---- connections ----

func (h *Handler) listConnections(c *gin.Context) {
	items, err := h.service.ListConnections(c.Request.Context())
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", gin.H{"items": items})
}

func (h *Handler) getConnection(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	item, err := h.service.GetConnection(c.Request.Context(), id)
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", item)
}

func (h *Handler) createConnection(c *gin.Context) {
	var input ConnectionInput
	if !bind(c, &input) {
		return
	}
	item, err := h.service.CreateConnection(c.Request.Context(), currentUser(c), input)
	if err != nil {
		fail(c, err)
		return
	}
	response.Created(c, "/api/v1/sql/connections", item.ID, item)
}

func (h *Handler) updateConnection(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var input ConnectionInput
	if !bind(c, &input) {
		return
	}
	item, err := h.service.UpdateConnection(c.Request.Context(), id, input)
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", item)
}

func (h *Handler) deleteConnection(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteConnection(c.Request.Context(), id); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) testConnection(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	extendDeadline(c, 30*time.Second)
	result, err := h.service.TestConnection(c.Request.Context(), id)
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", result)
}

// ---- query ----

func (h *Handler) query(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var input QueryRequest
	if !bind(c, &input) {
		return
	}
	input.Source = requestSource(c)
	budget := time.Duration(input.TimeoutSeconds) * time.Second
	if budget <= 0 || budget > 300*time.Second {
		budget = 300 * time.Second
	}
	extendDeadline(c, budget+15*time.Second)
	outcome, err := h.service.Query(c.Request.Context(), currentUser(c), id, input)
	if err != nil {
		fail(c, err)
		return
	}
	status := http.StatusOK
	if outcome.Status == "pending" {
		status = http.StatusAccepted
	}
	response.JSON(c, status, "OK", "success", outcome)
}

// checkQuery 预检 SQL 是否需要平台审批：无副作用，返回判定 + 预检令牌 + 指引。
func (h *Handler) checkQuery(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var input QueryRequest
	if !bind(c, &input) {
		return
	}
	outcome, err := h.service.CheckQuery(c.Request.Context(), id, input)
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", outcome)
}

func (h *Handler) getQuery(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	outcome, err := h.service.GetQuery(c.Request.Context(), id)
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", outcome)
}

func (h *Handler) listPending(c *gin.Context) {
	items, err := h.service.ListPending(c.Request.Context())
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", gin.H{"items": items})
}

func (h *Handler) approve(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	entry, err := h.service.Approve(c.Request.Context(), currentUser(c), id)
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", entry)
}

func (h *Handler) reject(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	entry, err := h.service.Reject(c.Request.Context(), currentUser(c), id)
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", entry)
}

// ---- introspection ----

func (h *Handler) listTables(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	extendDeadline(c, 30*time.Second)
	ctx, cancel := detachedCtx(20 * time.Second)
	defer cancel()
	tables, err := h.service.ListTables(ctx, currentUser(c), id)
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", gin.H{"items": tables})
}

func (h *Handler) tableSchema(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	name := c.Param("name")
	extendDeadline(c, 30*time.Second)
	ctx, cancel := detachedCtx(20 * time.Second)
	defer cancel()
	schema, err := h.service.DescribeTable(ctx, currentUser(c), id, name)
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", schema)
}

// ---- logs & settings ----

func (h *Handler) listLogs(c *gin.Context) {
	filter := LogFilter{Status: c.Query("status"), Kind: c.Query("kind")}
	filter.ConnectionID, _ = strconv.ParseInt(c.Query("connectionId"), 10, 64)
	filter.UserID, _ = strconv.ParseInt(c.Query("userId"), 10, 64)
	filter.Page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	filter.PageSize, _ = strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	for _, item := range []struct {
		name   string
		target **time.Time
	}{{"from", &filter.From}, {"to", &filter.To}} {
		if raw := c.Query(item.name); raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				response.Error(c, http.StatusBadRequest, "INVALID_REQUEST", fmt.Sprintf("%s 必须是 RFC3339 时间", item.name), nil)
				return
			}
			*item.target = &parsed
		}
	}
	page, err := h.service.ListLogs(c.Request.Context(), filter)
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", page)
}

func (h *Handler) getSettings(c *gin.Context) {
	settings, err := h.service.GetSettings(c.Request.Context())
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", settings)
}

func (h *Handler) updateSettings(c *gin.Context) {
	var input Settings
	if !bind(c, &input) {
		return
	}
	settings, err := h.service.UpdateSettings(c.Request.Context(), input)
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", settings)
}
