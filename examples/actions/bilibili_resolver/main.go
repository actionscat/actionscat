package main

import (
	"actionscat/pkg/actionscat"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type BiliVideoResponse struct {
	Code    int           `json:"code"`
	Message string        `json:"message"`
	Data    BiliVideoData `json:"data"`
}

type BiliVideoData struct {
	Bvid  string        `json:"bvid"`
	Title string        `json:"title"`
	Desc  string        `json:"desc"`
	Owner BiliOwner     `json:"owner"`
	Stat  BiliVideoStat `json:"stat"`
}

type BiliOwner struct {
	Mid  int64  `json:"mid"`
	Name string `json:"name"`
}

type BiliVideoStat struct {
	View     int `json:"view"`
	Danmaku  int `json:"danmaku"`
	Favorite int `json:"favorite"`
	Like     int `json:"like"`
}

func main() {
	appCtx := actionscat.GetContext()
	bvid := actionscat.GetEnv("PARAM_BVID")

	// If not injected via regex capture, attempt to find in message text
	if bvid == "" {
		re := regexp.MustCompile(`BV[0-9A-Za-z]{10}`)
		bvid = re.FindString(appCtx.Text)
	}

	if bvid == "" {
		log.Println("No BVID found in event")
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	url := fmt.Sprintf("https://api.bilibili.com/x/web-interface/view?bvid=%s", bvid)
	resp, err := client.Get(url)
	if err != nil {
		log.Printf("fetch bili error: %v", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var biliResp BiliVideoResponse
	if err := json.Unmarshal(body, &biliResp); err != nil || biliResp.Code != 0 {
		log.Printf("invalid bili response: code=%d err=%v", biliResp.Code, err)
		return
	}

	d := biliResp.Data
	replyText := fmt.Sprintf(
		"【哔哩哔哩】%s\nUP主：%s\n播放：%d | 弹幕：%d | 点赞：%d | 收藏：%d\nhttps://bilibili.com/video/%s",
		strings.TrimSpace(d.Title),
		d.Owner.Name,
		d.Stat.View,
		d.Stat.Danmaku,
		d.Stat.Like,
		d.Stat.Favorite,
		d.Bvid,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := actionscat.Reply(ctx, replyText); err != nil {
		log.Fatalf("reply failed: %v", err)
	}
	log.Println("Bilibili info resolved and replied successfully")
}
