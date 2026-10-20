package ir

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// 本文件实现 SSE（Server-Sent Events）的读写。
//
// 为什么放在 IR 包而不是各协议包：OpenAI 与 Anthropic 都用 SSE 作为传输框架，差别只在
// data 里的 JSON 形状与事件名。SSE 是字节流的分帧格式，不是 HTTP 语义，所以放在这里
// 不违反"IR 不认识 HTTP"；把它复制两份则会出现"一份处理 \r\n 一份不处理"的经典漂移。

// SSEEvent 是一个 SSE 帧。
type SSEEvent struct {
	// Event 是 `event:` 行的值；OpenAI 不发这一行（空字符串），Anthropic 用它区分事件类型。
	Event string
	// Data 是所有 `data:` 行按 \n 连接的结果（SSE 规范要求多行 data 用换行拼接）。
	Data string
	// ID 是 `id:` 行，重连语义用；我们目前不重连，保留是为了不改契约就能支持。
	ID string
	// Retry 是 `retry:` 行，同理。
	Retry string
}

// SSE 读写用的边界值。
const (
	// maxSSELineBytes 限制单行长度：上游若发一条没有换行的超大 JSON，逐行读取会把它整个
	// 吃进内存。给一个上限让这种流以错误结束，而不是把进程拖死。
	maxSSELineBytes = 1 << 20 // 1 MiB
	// sseDataPrefix 等前缀常量集中一处，避免两处实现各写一遍不同的大小写处理。
	sseDataPrefix  = "data:"
	sseEventPrefix = "event:"
	sseIDPrefix    = "id:"
	sseRetryPrefix = "retry:"
)

// SSEReader 逐帧读取 SSE 流。
type SSEReader struct {
	sc *bufio.Scanner
}

// NewSSEReader 构造读取器。
func NewSSEReader(r io.Reader) *SSEReader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxSSELineBytes)
	return &SSEReader{sc: sc}
}

// Next 返回下一帧。只有真正含 data 的帧才会返回；纯注释行（`: ping`）与空帧被跳过——
// Anthropic 会周期性发 `: ping`，把它当事件会让上层收到一堆空帧。
func (r *SSEReader) Next() (SSEEvent, error) {
	var ev SSEEvent
	var data []string
	started := false

	for r.sc.Scan() {
		line := r.sc.Text()
		// 兼容 \r\n：Scanner 的 ScanLines 已经去掉 \n，但 \r 会留下。
		line = strings.TrimSuffix(line, "\r")

		if line == "" {
			// 空行是帧分隔符。若这一帧收集到了 data，就交付；否则继续（可能是注释帧）。
			if started && len(data) > 0 {
				ev.Data = strings.Join(data, "\n")
				return ev, nil
			}
			// 重置：同一帧里 event 但没有 data（如 Anthropic 的某些空事件）不足以交付。
			ev = SSEEvent{}
			data = nil
			started = false
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // 注释/心跳
		}
		started = true
		switch {
		case strings.HasPrefix(line, sseDataPrefix):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, sseDataPrefix), " "))
		case strings.HasPrefix(line, sseEventPrefix):
			ev.Event = strings.TrimSpace(strings.TrimPrefix(line, sseEventPrefix))
		case strings.HasPrefix(line, sseIDPrefix):
			ev.ID = strings.TrimSpace(strings.TrimPrefix(line, sseIDPrefix))
		case strings.HasPrefix(line, sseRetryPrefix):
			ev.Retry = strings.TrimSpace(strings.TrimPrefix(line, sseRetryPrefix))
		}
	}
	if err := r.sc.Err(); err != nil {
		return SSEEvent{}, err
	}
	// 上游没发空行就断流：把最后收集到的 data 交付出去，否则最后一段内容会丢。
	if len(data) > 0 {
		ev.Data = strings.Join(data, "\n")
		return ev, nil
	}
	return SSEEvent{}, io.EOF
}

// SSEWriter 逐帧写出 SSE。
type SSEWriter struct {
	w io.Writer
}

// NewSSEWriter 构造写出器。
func NewSSEWriter(w io.Writer) *SSEWriter { return &SSEWriter{w: w} }

// Write 写出一帧并立即 Flush。
//
// 为什么每次都要 Flush：SSE 的意义就是增量到达；攒在 bufio 里会让客户端看到"停顿—爆发"，
// 而且一旦我们因为 C7 决定换候选，已缓冲未发出的内容反而成了状态负担。
func (w *SSEWriter) Write(ev SSEEvent) error {
	var b bytes.Buffer
	if ev.Event != "" {
		b.WriteString(sseEventPrefix)
		b.WriteByte(' ')
		b.WriteString(ev.Event)
		b.WriteByte('\n')
	}
	if ev.ID != "" {
		b.WriteString(sseIDPrefix)
		b.WriteByte(' ')
		b.WriteString(ev.ID)
		b.WriteByte('\n')
	}
	for _, line := range strings.Split(ev.Data, "\n") {
		b.WriteString(sseDataPrefix)
		b.WriteByte(' ')
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	if _, err := w.w.Write(b.Bytes()); err != nil {
		return err
	}
	if f, ok := w.w.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}
