// Package clamav 提供基于 clamd INSTREAM 协议的 ClamAV 病毒扫描客户端。
// 该实现只依赖标准库，通过 TCP 或 Unix socket 连接 clamd 守护进程；
// 任何连接、超时或协议错误都作为依赖不可用返回，绝不误报为“安全”。
package clamav

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

const (
	// instreamChunkSize 是 clamd INSTREAM 允许的单块上限（2048 字节）之内的
	// 保守取值；超过该值 clamd 会直接返回 INSTREAM size limit exceeded。
	instreamChunkSize = 1024
	commandTimeout   = 60 * time.Second
	responseLimit    = 256
)

// ErrInfected 表示 clamd 明确判定内容包含恶意软件。调用方必须把该错误与
// 依赖不可用错误区分处理：前者是确定的恶意结论，后者需要重试或失败关闭。
var ErrInfected = errors.New("clamav content is infected")

// Client 是 clamd INSTREAM 扫描客户端。network 支持 "tcp"（地址形如
// "clamav:3310"）和 "unix"（地址形如 "/var/run/clamav/clamd.ctl"）。
type Client struct {
	network string
	address string
}

// NewClient 构造 clamd 客户端并校验参数。
func NewClient(network, address string) (*Client, error) {
	network = strings.TrimSpace(network)
	address = strings.TrimSpace(address)
	switch network {
	case "tcp":
		if address == "" {
			return nil, errors.New("clamav tcp address is required")
		}
	case "unix":
		if address == "" {
			return nil, errors.New("clamav unix socket path is required")
		}
	default:
		return nil, fmt.Errorf("unsupported clamav network %q (use tcp or unix)", network)
	}
	return &Client{network: network, address: address}, nil
}

// Network 返回底层网络类型。
func (c *Client) Network() string { return c.network }

// Address 返回 clamd 地址。
func (c *Client) Address() string { return c.address }

// ScanStream 通过 INSTREAM 命令把 reader 的内容流式发送给 clamd 并返回判定。
// 返回 nil 表示内容安全；ErrInfected 表示检测到恶意软件；其他错误表示
// 扫描未完成（依赖不可用），调用方必须失败关闭而不是放行。
func (c *Client) ScanStream(ctx context.Context, content io.Reader) error {
	if c == nil {
		return errors.New("clamav client is not configured")
	}
	deadline := time.Now().Add(commandTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	dialer := net.Dialer{Deadline: deadline}
	connection, err := dialer.DialContext(ctx, c.network, c.address)
	if err != nil {
		return fmt.Errorf("connect clamd at %s/%s: %w", c.network, c.address, err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(deadline)
	if _, err = connection.Write([]byte("zINSTREAM\x00")); err != nil {
		return fmt.Errorf("send clamd INSTREAM command: %w", err)
	}
	buffer := make([]byte, instreamChunkSize)
	chunkHeader := make([]byte, 4)
	for {
		read, readErr := io.ReadFull(content, buffer)
		if read > 0 {
			binary.BigEndian.PutUint32(chunkHeader, uint32(read))
			if _, err = connection.Write(chunkHeader); err != nil {
				return fmt.Errorf("send clamd chunk header: %w", err)
			}
			if _, err = connection.Write(buffer[:read]); err != nil {
				return fmt.Errorf("send clamd chunk payload: %w", err)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
				break
			}
			return fmt.Errorf("read clamd payload: %w", readErr)
		}
	}
	binary.BigEndian.PutUint32(chunkHeader, 0)
	if _, err = connection.Write(chunkHeader); err != nil {
		return fmt.Errorf("terminate clamd stream: %w", err)
	}
	response, err := readNullTerminated(connection)
	if err != nil {
		return fmt.Errorf("read clamd verdict: %w", err)
	}
	return interpret(response)
}

// ScanBytes 扫描一段完整字节。
func (c *Client) ScanBytes(ctx context.Context, content []byte) error {
	return c.ScanStream(ctx, strings.NewReader(string(content)))
}

// Ping 使用 clamd PING 命令探测守护进程可用性；返回 nil 表示 clamd 响应正常。
func (c *Client) Ping(ctx context.Context) error {
	if c == nil {
		return errors.New("clamav client is not configured")
	}
	deadline := time.Now().Add(5 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	dialer := net.Dialer{Deadline: deadline}
	connection, err := dialer.DialContext(ctx, c.network, c.address)
	if err != nil {
		return fmt.Errorf("connect clamd at %s/%s: %w", c.network, c.address, err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(deadline)
	if _, err = connection.Write([]byte("zPING\x00")); err != nil {
		return fmt.Errorf("send clamd PING: %w", err)
	}
	response, err := readNullTerminated(connection)
	if err != nil {
		return fmt.Errorf("read clamd PONG: %w", err)
	}
	if strings.TrimSpace(response) != "PONG" {
		return fmt.Errorf("unexpected clamd PING response %q", response)
	}
	return nil
}

func readNullTerminated(connection net.Conn) (string, error) {
	builder := strings.Builder{}
	buffer := make([]byte, 1)
	for builder.Len() < responseLimit {
		read, err := connection.Read(buffer)
		if read > 0 {
			if buffer[0] == 0 {
				return builder.String(), nil
			}
			builder.WriteByte(buffer[0])
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && builder.Len() > 0 {
				return builder.String(), nil
			}
			return "", err
		}
	}
	return builder.String(), nil
}

func interpret(response string) error {
	verdict := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(response), "\x00"))
	if verdict == "stream: OK" {
		return nil
	}
	if strings.HasPrefix(verdict, "stream: ") {
		return fmt.Errorf("%w: %s", ErrInfected, strings.TrimSpace(strings.TrimPrefix(verdict, "stream: ")))
	}
	// clamd 对超限、内部错误等情况返回 ERROR 或 INSTREAM size limit 等文本；
	// 这些都不是“安全”结论，必须作为依赖不可用处理。
	return fmt.Errorf("clamd rejected scan: %q", verdict)
}
