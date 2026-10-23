package openai

// image.go —— 跨协议复用的图片 URL 解析（Chat 与 Responses 共用）。
//
// 真正的解码在 request.go 的 decodeImageURL（data URL 内联字节 / http(s)
// URL 保留引用）。这里是导出入口，供 v1responses.go（httpapi 层，Responses
// 入站）复用同一个 `capability` 口径——Responses 的 input_image 与 Chat 的
// image_url 是同一张图，解析规则必须一致，不能两处各写一份。

import "github.com/LittleFishStars/FreeRouter/internal/ir"

// DecodeImageURL 把 data URL 解码成字节、http(s) URL 保留引用（共用实现）。
func DecodeImageURL(u string) (ir.ImagePart, error) {
	return decodeImageURL(u)
}

// DecodeResponsesImage 是 Responses input_image 的化名（保持调点语义清晰）。
// 它只是 DecodeImageURL 的转发：Responses 的 input_image 结构也是 {url: ...}。
func DecodeResponsesImageURL(u string) (ir.ImagePart, error) {
	return DecodeImageURL(u)
}
