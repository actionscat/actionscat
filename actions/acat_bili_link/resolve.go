package acat_bili_link

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// BiliVideoResponse 定义视频 API 响应的顶层结构
type BiliVideoResponse struct {
	Code    int           `json:"code"`
	Message string        `json:"message"`
	Data    BiliVideoData `json:"data"`
}

// BiliVideoData 视频核心数据
type BiliVideoData struct {
	Bvid  string        `json:"bvid"`
	Title string        `json:"title"`
	Desc  string        `json:"desc"`
	Owner BiliOwner     `json:"owner"`
	Stat  BiliVideoStat `json:"stat"`
}

// BiliOwner UP主信息
type BiliOwner struct {
	Mid  int64  `json:"mid"`
	Name string `json:"name"`
}

// BiliVideoStat 视频统计信息
type BiliVideoStat struct {
	View     int `json:"view"`     // 播放量
	Danmaku  int `json:"danmaku"`  // 弹幕数
	Favorite int `json:"favorite"` // 收藏量
	Like     int `json:"like"`     // 点赞数
}

// BiliRelationResponse 定义用户关系 API 响应的结构
type BiliRelationResponse struct {
	Code int              `json:"code"`
	Data BiliRelationData `json:"data"`
}

// BiliRelationData 用户关系数据
type BiliRelationData struct {
	Follower int `json:"follower"` // 粉丝数
}

// VideoDetail 最终输出的聚合数据结构
type VideoDetail struct {
	Title       string
	UPName      string
	UPMid       int64
	UPFollowers int
	View        int
	Like        int
	Danmaku     int
	Favorite    int
	Description string
}

// httpClient 全局复用 HTTP 客户端
var httpClient = &http.Client{
	Timeout: 10 * time.Second,
}

// fetchJSON 发送 HTTP GET 请求并解析 JSON
func fetchJSON(url string, target interface{}) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	return json.Unmarshal(body, target)
}

// resolveVideo 解析 B 站视频综合信息
func resolveVideo(bvid string) (*VideoDetail, error) {
	// 1. 获取视频信息
	videoURL := fmt.Sprintf("https://api.bilibili.com/x/web-interface/view?bvid=%s", bvid)
	var videoResp BiliVideoResponse
	if err := fetchJSON(videoURL, &videoResp); err != nil {
		return nil, fmt.Errorf("failed to fetch video info: %v", err)
	}

	if videoResp.Code != 0 {
		return nil, fmt.Errorf("API error: %s (code: %d)", videoResp.Message, videoResp.Code)
	}

	data := videoResp.Data
	detail := &VideoDetail{
		Title:       data.Title,
		UPName:      data.Owner.Name,
		UPMid:       data.Owner.Mid,
		View:        data.Stat.View,
		Like:        data.Stat.Like,
		Danmaku:     data.Stat.Danmaku,
		Favorite:    data.Stat.Favorite,
		Description: data.Desc,
	}

	// 2. 获取 UP 主粉丝信息 (可选拓展)
	relationURL := fmt.Sprintf("https://api.bilibili.com/x/relation/stat?vmid=%d", detail.UPMid)
	var relationResp BiliRelationResponse
	if err := fetchJSON(relationURL, &relationResp); err == nil && relationResp.Code == 0 {
		detail.UPFollowers = relationResp.Data.Follower
	}

	return detail, nil
}
