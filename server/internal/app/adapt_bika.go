package app

import (
	"context"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"jmaura/internal/bika"
)

const bikaSource = "bika"

var bikaHTMLTagRe = regexp.MustCompile(`<[^>]*>`)

// bikaStripHTML 去掉富文本标签并压缩空白（评论描述常带 HTML）。
func bikaStripHTML(s string) string {
	s = bikaHTMLTagRe.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

// bikaStrings 把字段规整为字符串切片（兼容数组与单值两种上游形态）。
func bikaStrings(v any) []string {
	if l := bika.AsList(v); l != nil {
		return bika.AsStrList(l)
	}
	if s := strings.TrimSpace(bika.AsStr(v)); s != "" {
		return []string{s}
	}
	return nil
}

func bikaJoinText(v any) string { return strings.Join(bikaStrings(v), ", ") }

// bikaProxyURL 把外链图片转为同源代理地址（避免跨域/防盗链导致的加载失败）。
func bikaProxyURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	return "/api/image-proxy?url=" + url.QueryEscape(raw)
}

func bikaComicID(m map[string]any) string {
	if m == nil {
		return ""
	}
	if id := strings.TrimSpace(bika.AsStr(m["_id"])); id != "" {
		return id
	}
	return strings.TrimSpace(bika.AsStr(m["id"]))
}

// bikaCoverRawOf 按图片类型解析缩略图 URL。列表/详情用 cover 策略；
// 收藏接口的缩略图必须用 favourite 策略：storage1 会改路由到 s3.picacomic.com，
// 而 cover 策略指向的 img.picacomic.com 对这些静态路径会 404（实测）。
func bikaCoverRawOf(m map[string]any, pt bika.PictureType) string {
	thumb := bika.AsMap(m["thumb"])
	return bika.ImageURL(bika.AsStr(thumb["fileServer"]), bika.AsStr(thumb["path"]), pt)
}

func bikaCoverRaw(m map[string]any) string { return bikaCoverRawOf(m, bika.PictureCover) }

// bikaSummary 把 bika 漫画项映射为 v2ComicSummary（与 jm 完全同形，封面用 cover 策略）。
func bikaSummary(m map[string]any) v2ComicSummary {
	return bikaSummaryWith(m, bika.PictureCover)
}

func bikaSummaryWith(m map[string]any, pt bika.PictureType) v2ComicSummary {
	s := v2ComicSummary{
		Source:  bikaSource,
		ComicID: bikaComicID(m),
		Title:   bika.AsStr(m["title"]),
		Tags:    bikaStrings(m["tags"]),
		Raw:     m,
	}
	if a := bikaJoinText(m["author"]); a != "" {
		s.Author = v2StrPtr(a)
	}
	if c := bikaCoverRawOf(m, pt); c != "" {
		s.CoverURL = v2StrPtr(c)
	}
	if cats := bikaStrings(m["categories"]); len(cats) > 0 {
		s.Category = v2StrPtr(cats[0])
	}
	// 热度：榜单类响应（comics/leaderboard）带 totalViews / totalLikes / likesCount，
	// 普通列表没有这些字段，AsInt 会得到 0 —— 此时保持 nil，避免前端把 0 当成真实热度。
	if v := bika.AsInt(m["totalViews"]); v > 0 {
		views := v
		s.Views = &views
	}
	likes := bika.AsInt(m["likesCount"])
	if likes == 0 {
		likes = bika.AsInt(m["totalLikes"])
	}
	if likes > 0 {
		s.Likes = &likes
	}
	return s
}

// bikaDocs 从各种信封形状中提取漫画文档列表。
func bikaDocs(data any) []any {
	m := bika.AsMap(data)
	if m == nil {
		return bika.AsList(data)
	}
	if c := bika.AsMap(m["comics"]); c != nil {
		if docs := bika.AsList(c["docs"]); docs != nil {
			return docs
		}
	}
	if l := bika.AsList(m["comics"]); l != nil {
		return l
	}
	if comic := bika.AsMap(m["comic"]); comic != nil {
		return []any{comic}
	}
	if docs := bika.AsList(m["docs"]); docs != nil {
		return docs
	}
	return nil
}

// bikaSummaries 把任意信封映射为 v2ComicSummary 列表（封面用 cover 策略）。
func bikaSummaries(data any) []v2ComicSummary {
	return bikaSummariesWith(data, bika.PictureCover)
}

func bikaSummariesWith(data any, pt bika.PictureType) []v2ComicSummary {
	docs := bikaDocs(data)
	out := make([]v2ComicSummary, 0, len(docs))
	for _, d := range docs {
		m := bika.AsMap(d)
		if m == nil {
			continue
		}
		s := bikaSummaryWith(m, pt)
		if s.ComicID == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// bikaChapterKey 用 "comicId:order" 作为章节标识：读者页可从章节反查所属漫画。
func bikaChapterKey(comicID string, order int) string {
	return comicID + ":" + strconv.Itoa(order)
}

func bikaSplitChapterKey(key string) (string, int, bool) {
	i := strings.LastIndex(key, ":")
	if i <= 0 {
		return "", 0, false
	}
	order, err := strconv.Atoi(key[i+1:])
	if err != nil || order <= 0 {
		return "", 0, false
	}
	return key[:i], order, true
}

// bikaEpsAll 拉取全部章节并按 order 升序。
func bikaEpsAll(ctx context.Context, identity, comicID string, epsCount int) []map[string]any {
	totalPages := epsCount/40 + 1
	if totalPages < 1 {
		totalPages = 1
	}
	var docs []map[string]any
	for page := 1; page <= totalPages; page++ {
		p := page
		data, err := bikaCall(ctx, identity, func(t string) (any, error) {
			return bikaClient.ComicEps(ctx, t, comicID, p)
		})
		if err != nil {
			break
		}
		eps := bika.AsMap(bika.AsMap(data)["eps"])
		for _, d := range bika.AsList(eps["docs"]) {
			if m := bika.AsMap(d); m != nil {
				docs = append(docs, m)
			}
		}
	}
	sort.SliceStable(docs, func(i, j int) bool {
		return bika.AsInt(docs[i]["order"]) < bika.AsInt(docs[j]["order"])
	})
	return docs
}

// bikaComicDetailData 拉取并映射漫画详情（含章节列表）。
func bikaComicDetailData(ctx context.Context, identity, comicID string) (v2ComicDetail, error) {
	data, err := bikaCall(ctx, identity, func(t string) (any, error) {
		return bikaClient.ComicDetail(ctx, t, comicID)
	})
	if err != nil {
		return v2ComicDetail{}, err
	}
	comic := bika.AsMap(bika.AsMap(data)["comic"])
	if comic == nil {
		comic = bika.AsMap(data)
	}
	eps := bikaEpsAll(ctx, identity, comicID, bika.AsInt(comic["epsCount"]))
	chapters := make([]v2ChapterSummary, 0, len(eps))
	for _, ep := range eps {
		order := bika.AsInt(ep["order"])
		if order <= 0 {
			continue
		}
		chapters = append(chapters, v2ChapterSummary{
			ID:    bikaChapterKey(comicID, order),
			Title: bika.AsStr(ep["title"]),
			Order: order,
		})
	}
	out := v2ComicDetail{
		Source:  bikaSource,
		ComicID: comicID,
		Title:   bika.AsStr(comic["title"]),
		Tags:    bikaStrings(comic["tags"]),
		// 收藏态属于用户私有数据：未登录时恒为 false，绝不透出公共账号的收藏态。
		IsFavorite: bikaUserLoggedIn(identity) && bika.AsBool(comic["isFavourite"]),
		Chapters:   chapters,
		Raw:        comic,
	}
	if a := bikaJoinText(comic["author"]); a != "" {
		out.Author = v2StrPtr(a)
	}
	if c := bikaCoverRaw(comic); c != "" {
		out.CoverURL = v2StrPtr(c)
	}
	if d := bikaStripHTML(bika.AsStr(comic["description"])); d != "" {
		out.Description = v2StrPtr(d)
	}
	if cats := bikaStrings(comic["categories"]); len(cats) > 0 {
		out.Category = v2StrPtr(cats[0])
	}
	return out, nil
}

// bikaChapterDetailData 拉取并映射章节图片。scramble_id 固定为 0（bika 图片不置乱）。
func bikaChapterDetailData(ctx context.Context, identity, key string) (v2ChapterDetail, error) {
	comicID, order, ok := bikaSplitChapterKey(key)
	if !ok {
		return v2ChapterDetail{}, v2ProviderErr("无效的章节标识", 400)
	}
	var pages []any
	epTitle := ""
	for page := 1; page <= 50; page++ {
		p := page
		data, err := bikaCall(ctx, identity, func(t string) (any, error) {
			return bikaClient.ChapterPages(ctx, t, comicID, order, p)
		})
		if err != nil {
			if page == 1 {
				return v2ChapterDetail{}, err
			}
			break
		}
		m := bika.AsMap(data)
		pd := bika.AsMap(m["pages"])
		if ep := bika.AsMap(m["ep"]); ep != nil {
			if t := bika.AsStr(ep["title"]); t != "" {
				epTitle = t
			}
		}
		if docs := bika.AsList(pd["docs"]); docs != nil {
			pages = append(pages, docs...)
		}
		total := bika.AsInt(pd["pages"])
		if total <= 1 || page >= total {
			break
		}
	}
	images := make([]v2ChapterPage, 0, len(pages))
	for _, d := range pages {
		m := bika.AsMap(d)
		media := bika.AsMap(m["media"])
		raw := bika.ImageURL(bika.AsStr(media["fileServer"]), bika.AsStr(media["path"]), bika.PictureComic)
		name := bika.AsStr(media["originalName"])
		if name == "" {
			name = path.Base(bika.AsStr(media["path"]))
		}
		var u *string
		if raw != "" {
			u = v2StrPtr(bikaProxyURL(raw))
		}
		images = append(images, v2ChapterPage{Name: name, URL: u})
	}
	raw := map[string]any{
		"scramble_id": "0",
		"album_id":    comicID,
		"photo_id":    strconv.Itoa(order),
		"index":       order,
	}
	if epTitle != "" {
		raw["title"] = epTitle
	}
	return v2ChapterDetail{
		Source:    bikaSource,
		ChapterID: key,
		Title:     v2StrPtr(epTitle),
		Images:    images,
		Raw:       raw,
	}, nil
}
