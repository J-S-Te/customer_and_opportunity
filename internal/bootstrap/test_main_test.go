package bootstrap

import (
	"github.com/gin-gonic/gin"
	"os"
	"testing"
)

// Gin's mode is global. Set it before parallel tests create their engines;
// changing it inside a t.Parallel test races with unrelated router tests.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}
