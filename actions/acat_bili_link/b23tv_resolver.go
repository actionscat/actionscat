package acat_bili_link

import (
	"log"
	"net/http"
	"regexp"
	"time"
)

var (
	b23Regex = regexp.MustCompile(`https?://b23\.tv/[a-zA-Z0-9]+`)
)

// ========== 核心逻辑函数 ==========

// resolveB23 展开 b23.tv 短链（当遇到 302 时，不自动跟随跳转，而是返回当前响应）
func resolveB23(shortURL string) string {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 3 * time.Second,
	}

	resp, err := client.Get(shortURL)
	if err != nil {
		log.Printf("[b23] 请求失败: %v", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusFound {
		location := resp.Header.Get("Location")
		if bvid := bvRegex.FindString(location); bvid != "" {
			return bvid
		}
	}
	return ""
}
