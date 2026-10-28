package middleware

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

// maxRequestAuditBufferedResponseBytes 是强制审计模式下单请求响应的内存扣留上限，
// 超过即溢写临时文件继续扣留，不放行直写。
const maxRequestAuditBufferedResponseBytes = 64 << 10

// maxRequestAuditSpilledResponseBytes 是内存 + 临时文件的总硬上限；超过即视为无法在审计
// 确认前完整扣留响应，中间件按失败记录并拒绝请求。测试可覆盖以缩小阈值。
var maxRequestAuditSpilledResponseBytes = 256 << 20

var errRequestAuditResponseTooLarge = errors.New("request audit buffered response exceeds hard limit")

// requestAuditBuffer 在强制审计模式下扣留业务响应，直到 Complete 成功写入才提交给客户端。
//
// 与平台 SEC-D4b 同一语义：审计未确认 ⇒ 客户端拿不到成功。之前 Complete 失败只能记日志，
// 客户端照常收到成功响应，审计里只剩分发器补出的匿名“请求已开始→中断”记录，操作者、
// 路由与结果全部丢失；现在在强制模式下丢弃响应并返回 503。
type requestAuditBuffer struct {
	orig      gin.ResponseWriter
	headers   http.Header
	body      bytes.Buffer
	status    int
	size      int
	memLimit  int
	hardLimit int
	spill     *os.File
	total     int
	oversized bool
	logger    *slog.Logger
}

func newRequestAuditBuffer(orig gin.ResponseWriter, logger *slog.Logger) *requestAuditBuffer {
	if logger == nil {
		logger = slog.Default()
	}
	return &requestAuditBuffer{orig: orig, headers: make(http.Header), status: http.StatusOK, size: -1, memLimit: maxRequestAuditBufferedResponseBytes, hardLimit: maxRequestAuditSpilledResponseBytes, logger: logger}
}

func (b *requestAuditBuffer) flushTo(dst gin.ResponseWriter) {
	defer b.dispose()
	b.WriteHeaderNow()
	for key, values := range b.headers {
		dst.Header().Del(key)
		for _, value := range values {
			dst.Header().Add(key, value)
		}
	}
	dst.WriteHeader(b.status)
	if b.spill != nil {
		if _, err := b.spill.Seek(0, io.SeekStart); err != nil {
			b.logger.Error("request audit buffer: rewind spilled response", "error", err)
			return
		}
		if _, err := io.Copy(dst, b.spill); err != nil {
			b.logger.Error("request audit buffer: replay spilled response", "error", err)
		}
		return
	}
	if b.body.Len() > 0 {
		_, _ = dst.Write(b.body.Bytes())
		return
	}
	dst.WriteHeaderNow()
}

func (b *requestAuditBuffer) dispose() {
	if b.spill == nil {
		return
	}
	name := b.spill.Name()
	_ = b.spill.Close()
	if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		b.logger.Error("request audit buffer: remove spilled response", "path", name, "error", err)
	}
	b.spill = nil
}

func (b *requestAuditBuffer) spillToDisk() error {
	file, err := os.CreateTemp("", "request-audit-response-*.spill")
	if err != nil {
		return err
	}
	if b.body.Len() > 0 {
		if _, err := file.Write(b.body.Bytes()); err != nil {
			_ = file.Close()
			_ = os.Remove(file.Name())
			return err
		}
	}
	b.body.Reset()
	b.spill = file
	b.logger.Info("request audit buffered response exceeded memory limit; spilling to temporary file",
		"memory_limit_bytes", b.memLimit, "hard_limit_bytes", b.hardLimit, "status", b.status)
	return nil
}

func (b *requestAuditBuffer) Header() http.Header { return b.headers }

func (b *requestAuditBuffer) WriteHeader(code int) {
	if code > 0 && b.status != code && !b.Written() {
		b.status = code
	}
}

func (b *requestAuditBuffer) WriteHeaderNow() {
	if !b.Written() {
		b.size = 0
	}
}

func (b *requestAuditBuffer) Write(p []byte) (int, error) {
	if b.oversized {
		return 0, errRequestAuditResponseTooLarge
	}
	if b.total+len(p) > b.hardLimit {
		b.oversized = true
		b.logger.Warn("request audit buffered response exceeded hard limit; request will be rejected",
			"hard_limit_bytes", b.hardLimit, "status", b.status)
		return 0, errRequestAuditResponseTooLarge
	}
	b.WriteHeaderNow()
	if b.spill == nil && b.body.Len()+len(p) > b.memLimit {
		if err := b.spillToDisk(); err != nil {
			b.oversized = true
			b.logger.Error("request audit buffer: spill to temporary file", "error", err)
			return 0, errRequestAuditResponseTooLarge
		}
	}
	var n int
	var err error
	if b.spill != nil {
		n, err = b.spill.Write(p)
	} else {
		n, err = b.body.Write(p)
	}
	b.total += n
	b.size += n
	return n, err
}

func (b *requestAuditBuffer) WriteString(s string) (int, error) { return b.Write([]byte(s)) }

func (b *requestAuditBuffer) Status() int   { return b.status }
func (b *requestAuditBuffer) Size() int     { return b.size }
func (b *requestAuditBuffer) Written() bool { return b.size != -1 }

// Flush 只标记已写，绝不提前把半成品响应交给真实连接。
func (b *requestAuditBuffer) Flush() { b.WriteHeaderNow() }

func (b *requestAuditBuffer) Pusher() http.Pusher { return b.orig.Pusher() }

func (b *requestAuditBuffer) Hijack() (net.Conn, *bufio.ReadWriter, error) { return b.orig.Hijack() }

func (b *requestAuditBuffer) CloseNotify() <-chan bool { return b.orig.CloseNotify() }

func (b *requestAuditBuffer) Unwrap() http.ResponseWriter { return b.orig }
