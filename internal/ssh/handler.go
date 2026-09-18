package ssh

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"opsbox/internal/api/response"
	"opsbox/internal/platform/opscheck"
	"opsbox/internal/platform/sshx"
)

// localUserID 是本地单用户模式下的固定用户 ID（opsbox 无账号体系，审计里的操作人都记为它）。
const localUserID int64 = 1

// Handler 挂载 /api/v1/ssh/* 路由。
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
			ReadBufferSize:  32 * 1024,
			WriteBufferSize: 32 * 1024,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
	}
}

// RegisterRoutes 挂载全部路由。本地单用户：无登录、无权限码，中间件只剩 CORS。
func (h *Handler) RegisterRoutes(group *gin.RouterGroup) {
	g := group.Group("/ssh")
	g.GET("/connections", h.listConnections)
	g.POST("/connections", h.createConnection)
	g.GET("/connections/:id", h.getConnection)
	g.PUT("/connections/:id", h.updateConnection)
	g.DELETE("/connections/:id", h.deleteConnection)
	g.POST("/connections/:id/test", h.testConnection)
	g.POST("/connections/:id/exec", h.exec)
	g.POST("/connections/:id/exec/check", h.checkExec)
	g.POST("/connections/:id/transfers", h.requestTransfer)
	g.GET("/connections/:id/files/stat", h.statFile)
	g.GET("/connections/:id/files", h.download)
	g.PUT("/connections/:id/files", h.upload)
	g.POST("/connections/:id/sessions", h.openSession)
	g.GET("/connections/:id/sessions", h.listConnectionSessions)
	g.GET("/sessions", h.listSessions)
	g.POST("/connections/:id/sessions/:sid/ticket", h.sessionTicket)
	g.POST("/connections/:id/sessions/:sid/resize", h.resizeSession)
	g.POST("/connections/:id/sessions/:sid/rename", h.renameSession)
	g.DELETE("/connections/:id/sessions/:sid", h.closeSession)
	g.GET("/commands/:id", h.getCommand)
	g.GET("/pending-commands", h.listPending)
	g.POST("/commands/:id/approve", h.approve)
	g.POST("/commands/:id/reject", h.reject)
	g.GET("/exec-logs", h.listLogs)
	g.GET("/settings", h.getSettings)
	g.PUT("/settings", h.updateSettings)
}

// RegisterPublicRoutes 注册 WebSocket 端点：浏览器握手带不了 Authorization，凭一次性 ticket 鉴权。
func (h *Handler) RegisterPublicRoutes(group *gin.RouterGroup) {
	group.GET("/ssh/connections/:id/sessions/:sid/ws", h.sessionWS)
}

// ---- helpers ----

func currentUser(c *gin.Context) int64 {
	return localUserID
}

// requestSource 区分操作来源（cli / web）：浏览器请求（Wails WebView / dev 页面）带 Origin 头，
// CLI / 脚本没有。originGuard 已在服务器侧拒掉非本地来源，这里只做本地区分。
func requestSource(c *gin.Context) string {
	if c.Request.Header.Get("Origin") == "" {
		return "cli"
	}
	return "web"
}

// requireWebUI 审批操作只允许 UI（带 Origin 的浏览器请求）触发：CLI 不能批准自己提交的
// 写操作——批准权必须握在用户手里，这是审批队列的人机分离闸门。
func requireWebUI(c *gin.Context) bool {
	if requestSource(c) == "web" {
		return true
	}
	response.Error(c, http.StatusForbidden, "FORBIDDEN", "审批操作请在 opsbox 窗口中完成；CLI 不能批准自己提交的操作", nil)
	return false
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
	case errors.Is(err, ErrNotFound), errors.Is(err, sshx.ErrSessionNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "记录不存在"
	case errors.Is(err, ErrInvalid):
		status, code, message = http.StatusUnprocessableEntity, "INVALID_SSH_REQUEST", err.Error()
	case errors.Is(err, ErrDisabled):
		status, code, message = http.StatusConflict, "SSH_CONNECTION_DISABLED", err.Error()
	case errors.Is(err, ErrNoOpenSession):
		status, code = http.StatusConflict, "SSH_NO_OPEN_SESSION"
		message = "该连接没有已打开的终端会话。请先在 opsbox 窗口「SSH → 终端」打开该连接的终端，再执行命令（命令会在那个终端的连接上执行，你能实时看到命令与输出）。"
	case errors.Is(err, sshx.ErrExecBusy):
		status, code, message = http.StatusConflict, "SSH_EXEC_BUSY", err.Error()+"；请等它结束后再提交"
	case errors.Is(err, ErrConflict), errors.Is(err, sshx.ErrSessionAttached):
		status, code, message = http.StatusConflict, "CONFLICT", err.Error()
	case errors.Is(err, ErrForbidden):
		status, code, message = http.StatusForbidden, "FORBIDDEN", err.Error()
	case errors.Is(err, sshx.ErrHostKeyMismatch):
		status, code, message = http.StatusBadGateway, "SSH_HOST_KEY_MISMATCH", err.Error()
	case errors.As(err, new(*opscheck.RequiredError)):
		// 需审批命令缺少预检令牌：409 + 完整预检结果（含令牌），CLI 据此走「先确认再重提」协议
		var required *opscheck.RequiredError
		_ = errors.As(err, &required)
		response.JSON(c, http.StatusConflict, "SSH_CHECK_REQUIRED", required.Check.Guidance, required.Check)
		return
	default:
		// 拨号 / 认证失败等远端错误：把原因带给调用方（CLI 需要看到）
		if strings.Contains(err.Error(), "连接") || strings.Contains(err.Error(), "SSH") || strings.Contains(err.Error(), "私钥") || strings.Contains(err.Error(), "远端") {
			status, code, message = http.StatusBadGateway, "SSH_UPSTREAM_ERROR", err.Error()
		}
	}
	response.Error(c, status, code, message, nil)
}

// extendDeadline 放宽本次请求的网络死线（服务器默认 30s 写超时会打断长命令 / 大文件）。
func extendDeadline(c *gin.Context, budget time.Duration) {
	controller := http.NewResponseController(c.Writer)
	_ = controller.SetReadDeadline(time.Now().Add(budget))
	_ = controller.SetWriteDeadline(time.Now().Add(budget + 10*time.Second))
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
	response.Created(c, "/api/v1/ssh/connections", item.ID, item)
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

// ---- exec ----

func (h *Handler) exec(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var input ExecRequest
	if !bind(c, &input) {
		return
	}
	input.Source = requestSource(c)
	budget := time.Duration(input.TimeoutSeconds) * time.Second
	if budget <= 0 || budget > maxExecTimeout*time.Second {
		budget = maxExecTimeout * time.Second
	}
	extendDeadline(c, budget+15*time.Second)
	outcome, err := h.service.Exec(c.Request.Context(), currentUser(c), id, input)
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

// checkExec 预检命令是否需要平台审批：无副作用，返回判定 + 预检令牌 + 指引。
func (h *Handler) checkExec(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var input ExecRequest
	if !bind(c, &input) {
		return
	}
	outcome, err := h.service.CheckExec(c.Request.Context(), id, input)
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", outcome)
}

func (h *Handler) getCommand(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	outcome, err := h.service.GetCommand(c.Request.Context(), id)
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
	if !requireWebUI(c) {
		return
	}
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

// ---- transfers ----

func (h *Handler) requestTransfer(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var input TransferRequest
	if !bind(c, &input) {
		return
	}
	entry, err := h.service.RequestTransfer(c.Request.Context(), currentUser(c), id, input)
	if err != nil {
		fail(c, err)
		return
	}
	status := http.StatusOK
	if entry.Status == "pending" {
		status = http.StatusAccepted
	}
	response.JSON(c, status, "OK", "success", entry)
}

func (h *Handler) statFile(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	extendDeadline(c, 40*time.Second)
	info, err := h.service.StatRemote(c.Request.Context(), currentUser(c), id, c.Query("path"))
	if err != nil {
		fail(c, err)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", info)
}

// chunkParams 解析分片参数：transferId / offset / final。
func chunkParams(c *gin.Context) (TransferChunk, bool) {
	chunk := TransferChunk{Path: c.Query("path"), Final: c.Query("final") == "true"}
	for _, item := range []struct {
		name   string
		target *int64
	}{{"transferId", &chunk.TransferID}, {"offset", &chunk.Offset}} {
		raw := c.Query(item.name)
		if raw == "" {
			continue
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			response.Error(c, http.StatusBadRequest, "INVALID_REQUEST", item.name+" 无效", nil)
			return TransferChunk{}, false
		}
		*item.target = value
	}
	return chunk, true
}

// upload：PUT 原始字节流。大文件由 CLI 切片顺序上传（?offset=），最后一片带 ?final=true；
// 中断后先 GET files/stat 查远端已有大小，再从该偏移续传。
func (h *Handler) upload(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	chunk, ok := chunkParams(c)
	if !ok {
		return
	}
	extendDeadline(c, 30*time.Minute)
	result, entry, err := h.service.UploadChunk(c.Request.Context(), currentUser(c), id, chunk, c.Request.Body)
	if err != nil {
		fail(c, err)
		return
	}
	if entry.Status == "pending" {
		response.JSON(c, http.StatusAccepted, "OK", "success", entry)
		return
	}
	response.JSON(c, http.StatusOK, "OK", "success", result)
}

// download：GET ?path= 流式返回文件；带 ?offset= 时从该偏移续传（返回 206 与 Content-Range）。
func (h *Handler) download(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	chunk, ok := chunkParams(c)
	if !ok {
		return
	}
	extendDeadline(c, 30*time.Minute)
	started := false
	prepare := func(size int64) {
		started = true
		filename := path.Base(chunk.Path)
		c.Header("Content-Type", "application/octet-stream")
		c.Header("Content-Length", strconv.FormatInt(size-chunk.Offset, 10))
		c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
		c.Header("Accept-Ranges", "bytes")
		if chunk.Offset > 0 {
			c.Header("Content-Range", fmt.Sprintf("bytes %d-%d/%d", chunk.Offset, size-1, size))
			c.Status(http.StatusPartialContent)
			return
		}
		c.Status(http.StatusOK)
	}
	_, entry, err := h.service.DownloadChunk(c.Request.Context(), currentUser(c), id, chunk, prepare, c.Writer)
	if err != nil {
		if started {
			// 头已发出，只能中断连接让客户端按未完成处理
			c.Abort()
			return
		}
		fail(c, err)
		return
	}
	if entry.Status == "pending" {
		response.JSON(c, http.StatusAccepted, "OK", "success", entry)
	}
}

// ---- sessions ----

type sessionRequest struct {
	Rows      int   `json:"rows"`
	Cols      int   `json:"cols"`
	CommandID int64 `json:"commandId"`
	// SessionID 供 CLI 指定要接入的会话；留空则接入该连接最近活跃的那个。
	SessionID string `json:"sessionId"`
}

// openSession 开终端会话。CLI 令牌只能接入已有会话，不能新建——否则「必须先打开终端」
// 这道门禁自己就能被 CLI 绕过（开一个会话再往里执行命令）。开会话的权力留在浏览器端。
func (h *Handler) openSession(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var input sessionRequest
	if c.Request.ContentLength != 0 && !bind(c, &input) {
		return
	}
	extendDeadline(c, 30*time.Second)
	info, err := h.service.OpenSession(c.Request.Context(), currentUser(c), id, input.CommandID, input.Rows, input.Cols)
	if err != nil {
		fail(c, err)
		return
	}
	status := http.StatusCreated
	if info.Status == "pending" {
		status = http.StatusAccepted
	}
	response.JSON(c, status, "OK", "success", info)
}

// listConnectionSessions 列出某个连接上已打开的会话。
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

// listSessions 列出当前用户已打开的全部会话（CLI 用它决定能操作哪些连接）。
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

func (h *Handler) resizeSession(c *gin.Context) {
	var input struct {
		Rows int `json:"rows"`
		Cols int `json:"cols"`
	}
	if !bind(c, &input) {
		return
	}
	if err := h.service.ResizeSession(currentUser(c), c.Param("sid"), input.Rows, input.Cols); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) closeSession(c *gin.Context) {
	if err := h.service.CloseSession(currentUser(c), c.Param("sid")); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
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

// sessionWS 桥接 WebSocket 与 PTY：客户端帧（文本 / 二进制）原样写入 stdin，远端输出以二进制帧推送。
// 协议与 @xterm/addon-attach 一致，resize 走 REST。
func (h *Handler) sessionWS(c *gin.Context) {
	session, err := h.service.AttachSession(c.Query("ticket"))
	if err != nil {
		fail(c, err)
		return
	}
	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		session.Detach()
		return
	}
	defer conn.Close()
	defer session.Detach()
	// replay=true：断线重连 / 多端共享接入时先补回历史输出，不至于空屏
	subscriber := session.NewSubscriber(true)
	defer subscriber.Close()
	done := make(chan struct{})
	// stop 在浏览器断开后叫停推送协程：订阅通道不会关闭，没有它推送协程会一直挂在 select 上直到会话结束——
	// 持久会话可能活一小时以上，每次断开都会漏一个协程。
	stop := make(chan struct{})
	// 远端 → 浏览器
	go func() {
		defer close(done)
		for {
			select {
			case chunk := <-subscriber.C():
				_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
				if err := conn.WriteMessage(websocket.BinaryMessage, chunk); err != nil {
					return
				}
			case <-session.Done():
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				reason := "会话已结束"
				if err := session.Err(); err != nil {
					reason = "会话异常结束: " + err.Error()
				}
				_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason))
				return
			case <-stop:
				return
			}
		}
	}()
	// 浏览器 → 远端
	conn.SetReadLimit(64 * 1024)
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}
		if err := session.Write(data); err != nil {
			break
		}
	}
	// 浏览器断开：只叫停推送并 Detach（见 defer），不关会话——会话是持久的，重新打开面板还能接回
	close(stop)
	<-done
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
