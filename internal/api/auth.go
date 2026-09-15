package api

import (
	"actionscat/internal/domain"
	"actionscat/internal/store"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ManagementAuthMiddleware strictly isolates the management control plane
// from the runtime execution plane.
//
// Security invariants:
// 1. Missing authorization token -> 401 Unauthorized.
// 2. Presented token is a Run Capability Token -> 403 Forbidden (cross-domain privilege escalation rejected).
// 3. Forged / invalid management token -> 401 Unauthorized.
// 4. Valid management token -> authorized.
func ManagementAuthMiddleware(managementToken string, st *store.SQLiteStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		var rawToken string
		if cut, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
			rawToken = cut
		} else if tok := c.GetHeader("X-Actionscat-Management-Token"); tok != "" {
			rawToken = tok
		} else if tok := c.GetHeader("X-ActionsCat-Management-Token"); tok != "" {
			rawToken = tok
		} else if tok := c.GetHeader("X-ActionsCat-Token"); tok != "" {
			rawToken = tok
		}

		rawToken = strings.TrimSpace(rawToken)
		if rawToken == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "missing management authorization token",
			})
			return
		}

		// Security Boundary: Check if the presented token is a Run Capability Token.
		// Run Capability Tokens belong strictly to the runtime execution plane (/api/v1/runtime).
		// Any capability token attempting to access the management control plane must be rejected with 403 Forbidden.
		if st != nil {
			tokenHash := domain.HashToken(rawToken)
			if runTok, err := st.GetRunTokenByHash(c.Request.Context(), tokenHash); err == nil && runTok != nil {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
					"error": "run capability tokens cannot access the management API",
				})
				return
			}
		}

		// Verify against configured management token using constant-time comparison
		if managementToken == "" || subtle.ConstantTimeCompare([]byte(rawToken), []byte(managementToken)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "invalid management authorization token",
			})
			return
		}

		c.Next()
	}
}

// DispatchAuthMiddleware secures inbound message event ingestion (/v1/dispatch, /api/v1/dispatch).
//
// Security invariants:
// 1. Missing authorization token -> 401 Unauthorized.
// 2. Presented token is a Run Capability Token -> 403 Forbidden (capability tokens cannot trigger new runs).
// 3. Forged / invalid dispatch token -> 401 Unauthorized.
// 4. Valid dispatch token (or management token as administrative fallback) -> authorized.
func DispatchAuthMiddleware(dispatchToken string, managementToken string, st *store.SQLiteStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		var rawToken string
		if cut, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
			rawToken = cut
		} else if tok := c.GetHeader("X-Actionscat-Dispatch-Token"); tok != "" {
			rawToken = tok
		} else if tok := c.GetHeader("X-ActionsCat-Dispatch-Token"); tok != "" {
			rawToken = tok
		} else if tok := c.GetHeader("X-Actionscat-Ingress-Token"); tok != "" {
			rawToken = tok
		} else if tok := c.GetHeader("X-ActionsCat-Ingress-Token"); tok != "" {
			rawToken = tok
		} else if tok := c.GetHeader("X-ActionsCat-Token"); tok != "" {
			rawToken = tok
		}

		rawToken = strings.TrimSpace(rawToken)
		if rawToken == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "missing dispatch authorization token",
			})
			return
		}

		// Security Boundary: Check if the presented token is a Run Capability Token.
		// Run Capability Tokens belong strictly to the runtime execution plane (/api/v1/runtime).
		// Any capability token attempting to access the dispatch ingress plane must be rejected with 403 Forbidden.
		if st != nil {
			tokenHash := domain.HashToken(rawToken)
			if runTok, err := st.GetRunTokenByHash(c.Request.Context(), tokenHash); err == nil && runTok != nil {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
					"error": "run capability tokens cannot access the dispatch API",
				})
				return
			}
		}

		// Verify against configured dispatch token or management token
		var authorized bool
		if dispatchToken != "" && subtle.ConstantTimeCompare([]byte(rawToken), []byte(dispatchToken)) == 1 {
			authorized = true
		} else if managementToken != "" && subtle.ConstantTimeCompare([]byte(rawToken), []byte(managementToken)) == 1 {
			authorized = true
		}

		if !authorized {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "invalid dispatch authorization token",
			})
			return
		}

		c.Next()
	}
}
