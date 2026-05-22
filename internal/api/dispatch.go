package api

import (
	"actionscat/internal/matcher"
	"github.com/gin-gonic/gin"
	"log"
	"net/http"
)

type AdapterRequest struct {
	SenderQQ     string `json:"sender_qq"`
	CurrentGroup string `json:"current_group"`
	RawMsg       string `json:"raw_msg"`
}

type AdapterResponse struct {
	RespMsg string `json:"resp_msg"`
}

type ResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ResponseMessage struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type DispatchResponse struct {
	OK       bool              `json:"ok"`
	Error    *ResponseError    `json:"error,omitempty"`
	Action   string            `json:"action,omitempty"`
	Messages []ResponseMessage `json:"messages,omitempty"`
}

func DispatchHandler(c *gin.Context) {
	var req AdapterRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, DispatchResponse{
			OK:    false,
			Error: &ResponseError{Code: "BAD_REQUEST", Message: "invalid json body"},
		})
		return
	}

	log.Printf(
		"[adapter] received sender_qq=%s current_group=%s raw_msg=%q",
		req.SenderQQ,
		req.CurrentGroup,
		req.RawMsg,
	)

	// see if actions triggered
	matchResult, matched := matcher.GlobalEngine.Match(req.RawMsg)
	if !matched {
		c.JSON(http.StatusOK, DispatchResponse{OK: true})
		return
	}

	// find related executor
	executor, exists := matcher.GetExecutor(matchResult.ActionName)
	if !exists {
		// rule matched but no executor found, this is a config error
		c.JSON(http.StatusInternalServerError, DispatchResponse{
			OK: false, Error: &ResponseError{Code: "ERR_NO_EXEC", Message: "executor not found"},
		})
		return
	}

	// exec action logic
	ctx := matcher.ExecutionContext{
		RawMsg:       req.RawMsg,
		SenderQQ:     req.SenderQQ,
		CurrentGroup: req.CurrentGroup,
	}
	result, err := executor(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, DispatchResponse{
			OK: false, Error: &ResponseError{Code: "ERR_EXEC_FAILED", Message: err.Error()},
		})
		return
	}

	// conversion result
	var messages []ResponseMessage
	log.Printf("[dispatcher] result type: %T, value: %v", result, result)
	if msgs, ok := result.([]ResponseMessage); ok {
		messages = msgs
		log.Printf("[dispatcher] successfully converted to []ResponseMessage, count: %d", len(msgs))
	} else {
		log.Printf("[dispatcher] failed to convert result to []ResponseMessage")
	}

	// compose result to frontend
	c.JSON(http.StatusOK, DispatchResponse{
		OK:       true,
		Action:   matchResult.ActionName,
		Messages: messages,
	})
}
