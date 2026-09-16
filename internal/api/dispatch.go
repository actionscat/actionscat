package api

import (
	"actionscat/internal/domain"
	"actionscat/internal/matcher"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type IncomingEventRequest struct {
	// Platform-neutral fields
	Platform  string            `json:"platform,omitempty"`
	SessionID string            `json:"session_id,omitempty"`
	UserID    string            `json:"user_id,omitempty"`
	GroupID   string            `json:"group_id,omitempty"`
	Text      string            `json:"text,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`

	// Legacy adapter fields for backward compatibility
	SenderQQ     string `json:"sender_qq,omitempty"`
	CurrentGroup string `json:"current_group,omitempty"`
	RawMsg       string `json:"raw_msg,omitempty"`
}

func (req *IncomingEventRequest) Normalize() domain.MessageEvent {
	text := req.Text
	if text == "" && req.RawMsg != "" {
		text = req.RawMsg
	}

	userID := req.UserID
	if userID == "" && req.SenderQQ != "" {
		userID = req.SenderQQ
	}

	groupID := req.GroupID
	if groupID == "" && req.CurrentGroup != "" {
		groupID = req.CurrentGroup
	}

	platform := req.Platform
	if platform == "" {
		if req.SenderQQ != "" || req.CurrentGroup != "" {
			platform = "qq"
		} else {
			platform = "default"
		}
	}

	sessionID := req.SessionID
	if sessionID == "" {
		if groupID != "" && userID != "" {
			sessionID = fmt.Sprintf("%s:%s", groupID, userID)
		} else if groupID != "" {
			sessionID = groupID
		} else {
			sessionID = userID
		}
	}

	metadata := req.Metadata
	if metadata == nil {
		metadata = make(map[string]string)
	}

	return domain.MessageEvent{
		Platform:  strings.TrimSpace(platform),
		SessionID: strings.TrimSpace(sessionID),
		UserID:    strings.TrimSpace(userID),
		GroupID:   strings.TrimSpace(groupID),
		Text:      text,
		Metadata:  metadata,
	}
}

type DispatchResponse struct {
	OK      bool     `json:"ok"`
	Matched bool     `json:"matched"`
	RunIDs  []string `json:"run_ids,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type DispatchHandler struct {
	engine *matcher.Engine
}

func NewDispatchHandler(engine *matcher.Engine) *DispatchHandler {
	return &DispatchHandler{engine: engine}
}

func (h *DispatchHandler) HandleDispatch(c *gin.Context) {
	var req IncomingEventRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, DispatchResponse{
			OK:    false,
			Error: "invalid json body: " + err.Error(),
		})
		return
	}

	event := req.Normalize()
	if event.Text == "" {
		c.JSON(http.StatusBadRequest, DispatchResponse{
			OK:    false,
			Error: "message text cannot be empty",
		})
		return
	}

	runs, err := h.engine.MatchAndDispatch(c.Request.Context(), event)
	if err != nil {
		c.JSON(http.StatusInternalServerError, DispatchResponse{
			OK:    false,
			Error: "matching failed: " + err.Error(),
		})
		return
	}

	matched := len(runs) > 0
	runIDs := make([]string, len(runs))
	for i, r := range runs {
		runIDs[i] = r.ID
	}

	c.JSON(http.StatusOK, DispatchResponse{
		OK:      true,
		Matched: matched,
		RunIDs:  runIDs,
	})
}
