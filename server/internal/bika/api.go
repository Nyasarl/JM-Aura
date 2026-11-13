package bika

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Sort 允许的取值：dd(最新) / da(最旧) / ld(最多喜欢) / vd(最多观看)。
func sortOr(sort string) string {
	if sort == "" {
		return "dd"
	}
	return sort
}

// SignIn 用邮箱+密码换取 token。
func (c *Client) SignIn(ctx context.Context, email, password string) (string, error) {
	data, err := c.Request(ctx, http.MethodPost, "/auth/sign-in", nil, map[string]any{
		"email":    email,
		"password": password,
	}, "")
	if err != nil {
		return "", err
	}
	token := AsStr(AsMap(data)["token"])
	if token == "" {
		return "", &APIError{Message: "bika: 登录未返回 token"}
	}
	return token, nil
}

// RegisterParams 是 /auth/register 的请求字段。
type RegisterParams struct {
	Name      string
	Email     string
	Password  string
	Question1 string
	Answer1   string
	Question2 string
	Answer2   string
	Question3 string
	Answer3   string
	Birthday  string
	Gender    string
}

// Register 在上游创建账号（实测：邮箱可填非邮箱串、gender 可为 "bot"、无邮箱验证）。
// 成功时上游返回 {code:200,message:"success"}，信封里没有 data，因此只回错误。
func (c *Client) Register(ctx context.Context, p RegisterParams) error {
	_, err := c.Request(ctx, http.MethodPost, "/auth/register", nil, map[string]any{
		"name":      p.Name,
		"email":     p.Email,
		"password":  p.Password,
		"question1": p.Question1,
		"answer1":   p.Answer1,
		"question2": p.Question2,
		"answer2":   p.Answer2,
		"question3": p.Question3,
		"answer3":   p.Answer3,
		"birthday":  p.Birthday,
		"gender":    p.Gender,
	}, "")
	return err
}

// Categories 获取分类表（data.categories）。
func (c *Client) Categories(ctx context.Context, token string) (any, error) {
	return c.Request(ctx, http.MethodGet, "/categories", nil, nil, token)
}

// Keywords 获取热搜词（data.keywords）。
func (c *Client) Keywords(ctx context.Context, token string) (any, error) {
	return c.Request(ctx, http.MethodGet, "/keywords", nil, nil, token)
}

// AdvancedSearch 高级搜索：POST comics/advanced-search?page=N。
func (c *Client) AdvancedSearch(ctx context.Context, token, keyword string, page int, categories []string, sort string) (any, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	if categories == nil {
		categories = []string{}
	}
	body := map[string]any{
		"sort":       sortOr(sort),
		"keyword":    keyword,
		"categories": categories,
	}
	return c.Request(ctx, http.MethodPost, "/comics/advanced-search", q, body, token)
}

// ComicsByCategory 按分类浏览：GET comics?page=N&c={分类}&s={排序}。
// category 为空或 "0" 时省略 c（即全站最近更新流）。
func (c *Client) ComicsByCategory(ctx context.Context, token, category, sort string, page int) (any, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	if c2 := strings.TrimSpace(category); c2 != "" && c2 != "0" {
		q.Set("c", c2)
	}
	q.Set("s", sortOr(sort))
	return c.Request(ctx, http.MethodGet, "/comics", q, nil, token)
}

// ComicsByCreator 按创作者浏览：GET comics?ca={id}&s={排序}&page=N。
func (c *Client) ComicsByCreator(ctx context.Context, token, creatorID, sort string, page int) (any, error) {
	q := url.Values{}
	q.Set("ca", creatorID)
	q.Set("s", sortOr(sort))
	q.Set("page", strconv.Itoa(page))
	return c.Request(ctx, http.MethodGet, "/comics", q, nil, token)
}

// Leaderboard 排行榜：GET comics/leaderboard?tt={tt}&ct={ct}。
//
// tt 为时间段（App 端 LeaderboardTimeframe：H24=24小时 / D7=7天 / D30=30天），
// ct 为榜单类型（App 端 LeaderboardCategory 目前只有 VIEW_COUNT="VC"=浏览榜）。
// 上游该接口不分页，一次返回固定长度的榜单；返回体为 data.comics[]。
func (c *Client) Leaderboard(ctx context.Context, token, tt, ct string) (any, error) {
	if tt == "" {
		tt = "H24"
	}
	if ct == "" {
		ct = "VC"
	}
	q := url.Values{}
	q.Set("tt", tt)
	q.Set("ct", ct)
	return c.Request(ctx, http.MethodGet, "/comics/leaderboard", q, nil, token)
}

// KnightLeaderboard 创作者榜：GET comics/knight-leaderboard。
func (c *Client) KnightLeaderboard(ctx context.Context, token string) (any, error) {
	return c.Request(ctx, http.MethodGet, "/comics/knight-leaderboard", nil, nil, token)
}

// Random 随机本子：GET comics/random。
func (c *Client) Random(ctx context.Context, token string) (any, error) {
	return c.Request(ctx, http.MethodGet, "/comics/random", nil, nil, token)
}

// ComicDetail 详情：GET comics/{id} → data.comic。
func (c *Client) ComicDetail(ctx context.Context, token, comicID string) (any, error) {
	return c.Request(ctx, http.MethodGet, "/comics/"+url.PathEscape(comicID), nil, nil, token)
}

// ComicEps 章节列表（分页，每页 40）：GET comics/{id}/eps?page=N → data.eps.docs。
func (c *Client) ComicEps(ctx context.Context, token, comicID string, page int) (any, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	return c.Request(ctx, http.MethodGet, "/comics/"+url.PathEscape(comicID)+"/eps", q, nil, token)
}

// ComicRecommend 相关推荐：GET comics/{id}/recommendation → data.comics。
func (c *Client) ComicRecommend(ctx context.Context, token, comicID string) (any, error) {
	return c.Request(ctx, http.MethodGet, "/comics/"+url.PathEscape(comicID)+"/recommendation", nil, nil, token)
}

// ChapterPages 章节图片：GET comics/{id}/order/{order}/pages?page=N → data.pages.docs。
func (c *Client) ChapterPages(ctx context.Context, token, comicID string, order, page int) (any, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	p := fmt.Sprintf("/comics/%s/order/%d/pages", url.PathEscape(comicID), order)
	return c.Request(ctx, http.MethodGet, p, q, nil, token)
}

// Favorites 云端收藏：GET users/favourite?s={排序}&page=N → data.comics。
func (c *Client) Favorites(ctx context.Context, token, sort string, page int) (any, error) {
	q := url.Values{}
	q.Set("s", sortOr(sort))
	q.Set("page", strconv.Itoa(page))
	return c.Request(ctx, http.MethodGet, "/users/favourite", q, nil, token)
}

// ToggleFavorite 收藏/取消收藏：POST comics/{id}/favourite。
func (c *Client) ToggleFavorite(ctx context.Context, token, comicID string) (any, error) {
	return c.Request(ctx, http.MethodPost, "/comics/"+url.PathEscape(comicID)+"/favourite", nil, map[string]any{}, token)
}

// ToggleLike 喜欢/取消喜欢：POST comics/{id}/like。
func (c *Client) ToggleLike(ctx context.Context, token, comicID string) (any, error) {
	return c.Request(ctx, http.MethodPost, "/comics/"+url.PathEscape(comicID)+"/like", nil, map[string]any{}, token)
}

// Comments 主评论流：GET comics/{id}/comments?page=N → data.comments.docs / data.topComments。
func (c *Client) Comments(ctx context.Context, token, comicID string, page int) (any, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	return c.Request(ctx, http.MethodGet, "/comics/"+url.PathEscape(comicID)+"/comments", q, nil, token)
}

// CommentChildren 子评论：GET comments/{id}/childrens?page=N。
func (c *Client) CommentChildren(ctx context.Context, token, commentID string, page int) (any, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	return c.Request(ctx, http.MethodGet, "/comments/"+url.PathEscape(commentID)+"/childrens", q, nil, token)
}

// PostComment 发表评论：POST comics/{id}/comments。
func (c *Client) PostComment(ctx context.Context, token, comicID, content string) (any, error) {
	return c.Request(ctx, http.MethodPost, "/comics/"+url.PathEscape(comicID)+"/comments", nil, map[string]any{"content": content}, token)
}

// PostReply 回复评论：POST comments/{id}。
func (c *Client) PostReply(ctx context.Context, token, commentID, content string) (any, error) {
	return c.Request(ctx, http.MethodPost, "/comments/"+url.PathEscape(commentID), nil, map[string]any{"content": content}, token)
}

// Profile 账号资料：GET users/profile。
func (c *Client) Profile(ctx context.Context, token string) (any, error) {
	return c.Request(ctx, http.MethodGet, "/users/profile", nil, nil, token)
}

// PunchIn 签到：POST users/punch-in。
func (c *Client) PunchIn(ctx context.Context, token string) (any, error) {
	return c.Request(ctx, http.MethodPost, "/users/punch-in", nil, map[string]any{}, token)
}
