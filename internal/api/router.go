package api

import (
	"github.com/gin-gonic/gin"
)

func NewRouter() *gin.Engine {
	r := gin.Default()

	r.GET("/healthz", HealthzHandler)
	r.POST("/v1/dispatch", DispatchHandler)

	return r
}

func HealthzHandler(c *gin.Context) {
	c.String(200, "ok")
}
