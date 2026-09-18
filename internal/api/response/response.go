package response

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

type Envelope struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Data      any    `json:"data,omitempty"`
	Details   any    `json:"details,omitempty"`
	RequestID string `json:"requestId"`
}

// FieldError describes a single request field validation failure.
type FieldError struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

func JSON(c *gin.Context, status int, code string, message string, data any) {
	c.JSON(status, Envelope{Code: code, Message: message, Data: data, RequestID: requestID(c)})
}

func Created(c *gin.Context, resourcePath string, id int64, data any) {
	c.Header("Location", resourcePath+"/"+strconv.FormatInt(id, 10))
	JSON(c, 201, "OK", "success", data)
}

func Error(c *gin.Context, status int, code string, message string, details any) {
	c.JSON(status, Envelope{Code: code, Message: message, Details: details, RequestID: requestID(c)})
}

func requestID(c *gin.Context) string {
	value, _ := c.Get("request_id")
	requestID, _ := value.(string)
	return requestID
}
