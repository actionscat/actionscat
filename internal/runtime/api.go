package runtime

import (
	"actionscat/internal/domain"
	"actionscat/internal/frostagent"
	"actionscat/internal/store"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	maxStateWriteBytes = 5 * 1024 * 1024 // 5 MB
)

type contextKey string

const runTokenContextKey contextKey = "run_token"

type API struct {
	store      *store.SQLiteStore
	stateStore *store.StateStore
	frostagent frostagent.Client
}

func NewAPI(store *store.SQLiteStore, stateStore *store.StateStore, fa frostagent.Client) *API {
	return &API{
		store:      store,
		stateStore: stateStore,
		frostagent: fa,
	}
}

// RegisterRoutes registers runtime API routes on a Gin router group.
func (a *API) RegisterRoutes(rg *gin.RouterGroup) {
	rg.Use(a.AuthenticateMiddleware())
	rg.POST("/state", a.HandleWriteState)
	rg.POST("/frostagent/send", a.HandleFrostAgentSend)
}

// AuthenticateMiddleware verifies the single-run capability token.
func (a *API) AuthenticateMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		var rawToken string
		if cut, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
			rawToken = cut
		} else if tok := c.GetHeader("X-ActionsCat-Token"); tok != "" {
			rawToken = tok
		}

		rawToken = strings.TrimSpace(rawToken)
		if rawToken == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing runtime capability token"})
			return
		}

		hash := domain.HashToken(rawToken)
		token, err := a.store.GetRunTokenByHash(c.Request.Context(), hash)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid runtime capability token"})
			return
		}

		now := time.Now().UTC()
		if !token.IsValid(now) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "runtime capability token has expired or been revoked"})
			return
		}

		// Also verify that the Run is currently active (running)
		run, err := a.store.GetRun(c.Request.Context(), token.RunID)
		if err != nil || run.Status != domain.RunStatusRunning {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "run is no longer active"})
			return
		}

		// Store token in request context
		ctx := context.WithValue(c.Request.Context(), runTokenContextKey, token)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func GetToken(c *gin.Context) *domain.RunToken {
	if val := c.Request.Context().Value(runTokenContextKey); val != nil {
		if tok, ok := val.(*domain.RunToken); ok {
			return tok
		}
	}
	return nil
}

type WriteStateRequest struct {
	// ActionID might be sent by malicious client code, but MUST BE COMPLETELY IGNORED
	ActionID string `json:"action_id,omitempty"`
	Path     string `json:"path"`
	Data     string `json:"data"` // string or base64
	IsBase64 bool   `json:"is_base64"`
}

func (a *API) HandleWriteState(c *gin.Context) {
	tok := GetToken(c)
	if tok == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	if !tok.HasScope(domain.ScopeStateWrite) {
		c.JSON(http.StatusForbidden, gin.H{"error": "token lacks state.write scope"})
		return
	}

	var req WriteStateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json body: " + err.Error()})
		return
	}

	if strings.TrimSpace(req.Path) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path cannot be empty"})
		return
	}

	var dataBytes []byte
	if req.IsBase64 {
		b, err := base64.StdEncoding.DecodeString(req.Data)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid base64 data"})
			return
		}
		dataBytes = b
	} else {
		dataBytes = []byte(req.Data)
	}

	if len(dataBytes) > maxStateWriteBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "state payload exceeds maximum size"})
		return
	}

	// CRITICAL SECURITY INVARIANT:
	// Action ID is derived STRICTLY from tok.ActionID, NEVER from request body!
	err := a.stateStore.WriteState(tok.ActionID, req.Path, dataBytes)
	if err != nil {
		if errors.Is(err, store.ErrPathTraversal) || errors.Is(err, store.ErrInvalidPath) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid or dangerous state path: " + err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to write state: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (a *API) HandleFrostAgentSend(c *gin.Context) {
	tok := GetToken(c)
	if tok == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	if !tok.HasScope(domain.ScopeFrostAgentSendMsg) {
		c.JSON(http.StatusForbidden, gin.H{"error": "token lacks frostagent.sendmsg scope"})
		return
	}

	var req frostagent.SendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json body: " + err.Error()})
		return
	}

	if len(req.Messages) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "messages cannot be empty"})
		return
	}

	// Core proxies to FrostAgent using Core's long-lived credentials
	err := a.frostagent.SendMessage(c.Request.Context(), req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "frostagent delivery failed: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}
