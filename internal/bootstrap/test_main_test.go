package bootstrap

import (
	"github.com/gin-gonic/gin"
	"os"
	"testing"
)

// Gin 模式为全局状态，须在并行测试创建引擎前设置；
// 在 t.Parallel 测试中修改会与其他路由测试发生竞争。
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}
