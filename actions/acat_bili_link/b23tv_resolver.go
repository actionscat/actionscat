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

// resolveB23 unfold bilibili short link
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
