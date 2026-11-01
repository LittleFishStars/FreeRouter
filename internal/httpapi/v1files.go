package httpapi

// v1files.go —— Files 与 Search & Fetch 端点的 handler（原样转发）。
//
// 六个 handler 都只是 servePassthrough 的一行调用：httpapi 这一层不解析
// 请求体、不校验字段、不转换响应。差异只有两个字符串——上游路径与 HTTP 方法。
//
// 上游路径逐条对齐 DeepSeek 官方 API：
//   POST   /files              上传文件（multipart，最大 64 MiB）
//   GET    /files              列出文件
//   GET    /files/{id}         取回文件元数据
//   DELETE /files/{id}         删除文件
//   POST   /v1/tools/search    联网搜索
//   POST   /v1/tools/fetch     抓取网页内容
//
// 入站路径与上游路径**同名**（官方就是这么设计的），所以这里不做映射表：
// 双方的路径是一种，多写一张表只会让两者漂移。

import "net/http"

// HandleV1FileUpload 处理 POST /files（multipart 上传）。
//
// 为什么这里不读 body：multipart 边界与 64 MiB 的体积都该由上游判。
// 网关一读就要缓冲整份图片，而透传的价值恰恰在于"不碰它"。
func (s *Server) HandleV1FileUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeV1Error(w, "invalid_request", "方法不允许", requestIDOf(r))
		return
	}
	s.servePassthrough(w, r, "/files")
}

// HandleV1FileList 处理 GET /files。
func (s *Server) HandleV1FileList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeV1Error(w, "invalid_request", "方法不允许", requestIDOf(r))
		return
	}
	s.servePassthrough(w, r, "/files")
}

// HandleV1FileRetrieve 处理 GET /files/{id}。
func (s *Server) HandleV1FileRetrieve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeV1Error(w, "invalid_request", "方法不允许", requestIDOf(r))
		return
	}
	// 路径段原样拼回上游：id 里可能有编码字符（file-api-… 形态虽简单，
	// 但透传的原则是不替上游解释 id），所以取原始 path 而不是解析参数。
	s.servePassthrough(w, r, "/files/"+pathTail(r.URL.Path))
}

// HandleV1FileDelete 处理 DELETE /files/{id}。
func (s *Server) HandleV1FileDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		s.writeV1Error(w, "invalid_request", "方法不允许", requestIDOf(r))
		return
	}
	s.servePassthrough(w, r, "/files/"+pathTail(r.URL.Path))
}

// HandleV1Search 处理 POST /v1/tools/search（联网搜索）。
//
// 为什么不做查询改写："site:"、"-site:"、引号短语这些运算符是上游的语法，
// 网关改写查询等于替用户改问题——而且上游的排序分（rerank_score）也会随之失真。
func (s *Server) HandleV1Search(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeV1Error(w, "invalid_request", "方法不允许", requestIDOf(r))
		return
	}
	s.servePassthrough(w, r, "/v1/tools/search")
}

// HandleV1Fetch 处理 POST /v1/tools/fetch（抓取网页内容）。
func (s *Server) HandleV1Fetch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeV1Error(w, "invalid_request", "方法不允许", requestIDOf(r))
		return
	}
	s.servePassthrough(w, r, "/v1/tools/fetch")
}

// pathTail 取 URL 路径的最后一段（用于把 {id} 原样带到上游）。
//
// 为什么不用 chi 的 URLParam：这些 handler 也会被测试直调（不经过路由树），
// 那时 URLParam 是空的。从 Path 取尾段让两条调用方式行为一致。
func pathTail(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}
