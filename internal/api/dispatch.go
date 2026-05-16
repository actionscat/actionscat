package api

import (
	"fmt"
	"github.com/gin-gonic/gin"
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

	//assemble resp data
	resp := DispatchResponse{
		OK:     true,
		Action: "debug_echo",
		Messages: []ResponseMessage{
			{
				Type: "text",
				Text: fmt.Sprintf("Actioncat core received msg from %s in group %s", req.SenderQQ, req.CurrentGroup),
			},
		},
	}

	c.JSON(http.StatusOK, resp)
}
