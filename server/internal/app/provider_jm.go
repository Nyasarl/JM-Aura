package app

import (
	"context"
	"math/rand"
	"net/url"
	"strconv"
	"strings"
	"time"

	"jmaura/internal/provider"
	"jmaura/internal/store"
)

func init() {
	provider.Register(jmProvider{})
}

// jmProvider 把现有的 JM 只读内容能力包装成 provider.Provider 实现。
//
// 方法体是原 v2 handler 逻辑的逐字搬运，除「写响应」改为「返回 (any, error)」外
// 不做任何行为改动，以保证接入 provider 层后 JM 零回归。
type jmProvider struct{}

func (jmProvider) Source() provider.Source { return provider.SourceJM }

func (jmProvider) Caps() provider.Caps {
	return provider.Caps{
		Search:         true,
		Categories:     true,
		Leaderboard:    true,
		Random:         true,
		ComicDetail:    true,
		Chapter:        true,
		AlsoViewed:     true,
		Favorite:       true,
		FavoriteFolder: true,
		Like:           true,
		Comment:        true,
		CommentLike:    true,
		Novel:          true,
		NovelExport:    true,
		Download:       true,
		Checkin:        true,
	}
}

func (jmProvider) Categories(ctx context.Context, identity string) (any, error) {
	res, err := apiClient().Categories(ctx, store.LoadCookies(identity))
	if err != nil {
		return nil, err
	}
	out := []any{}
	if m := rawMap(decodeAny(res.Data)); m != nil {
		if l := rawList(m["categories"]); len(l) > 0 {
			out = l
		} else if l2 := rawList(m["data"]); len(l2) > 0 {
			out = l2
		}
	}
	return out, nil
}

func (jmProvider) Search(ctx context.Context, identity, keyword string, page int) (any, error) {
	sq := url.Values{}
	sq.Set("search_query", keyword)
	sq.Set("o", "mr")
	if page > 1 {
		sq.Set("page", strconv.Itoa(page))
	}
	res, err := apiClient().APIGet(ctx, "/search", sq, store.LoadCookies(identity))
	if err != nil {
		return nil, err
	}
	raw := decodeAny(res.Data)
	if dm, isMap := raw.(map[string]any); isMap {
		if aid := strings.TrimSpace(pyStr(dm["redirect_aid"])); aid != "" {
			aq := url.Values{}
			aq.Set("comicName", "")
			aq.Set("id", aid)
			ares, aerr := apiClient().APIGet(ctx, "/album", aq, store.LoadCookies(identity))
			if aerr == nil {
				if album := adaptAlbumDetail(decodeRawMap(ares.Data)); pyStr(album["album_id"]) != "" {
					return []v2ComicSummary{v2SummaryFromAdapt(album, true)}, nil
				}
			}
		}
	}
	items := adaptSearchResult(raw)
	out := make([]v2ComicSummary, 0, len(items))
	for _, it := range items {
		if pyStr(it["album_id"]) == "" {
			continue
		}
		out = append(out, v2SummaryFromAdapt(it, true))
	}
	return out, nil
}

func (jmProvider) Leaderboard(ctx context.Context, identity string, q provider.LeaderboardQuery) (any, error) {
	return v2LeaderboardItems(ctx, store.LoadCookies(identity), q.Category, q.Page, q.Sort, q.Tag)
}

// Random 返回随机页一次呈现的 10 本。JM 上游没有批量随机接口，这里随机挑若干
// （分类 × 排序 × 页码）榜单页，打散去重后取前 10；榜单全空时退化为「最新」流。
func (jmProvider) Random(ctx context.Context, identity string) (any, error) {
	ck := store.LoadCookies(identity)
	catIDs := v2FetchCategoryIDs(ctx, ck)
	sorts := []string{"mr", "tf", "mv", "mp"}

	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	pool := []v2ComicSummary{}
	for i := 0; i < 8; i++ {
		items, lerr := v2LeaderboardItems(ctx, ck, catIDs[rand.Intn(len(catIDs))], rand.Intn(50)+1, sorts[rand.Intn(len(sorts))], "")
		if lerr != nil {
			continue
		}
		pool = append(pool, items...)
		if len(pool) >= v2RandomTarget {
			break
		}
	}
	if out := v2PickUnique(rnd, pool, v2RandomTarget); len(out) > 0 {
		return out, nil
	}

	// 榜单页全空时退化为「最新」流，尽量仍给出 10 本。
	lres, lerr := apiClient().Latest(ctx, "0", ck)
	if lerr != nil {
		return nil, lerr
	}
	raw := decodeAny(lres.Data)
	fallback := []v2ComicSummary{}
	if list, isList := raw.([]any); isList {
		for _, x := range list {
			if m := rawMap(x); m != nil {
				if s, ok2 := v2SummaryFromLatestItem(m); ok2 {
					fallback = append(fallback, s)
				}
			}
		}
	}
	if len(fallback) == 0 {
		for _, it := range adaptSearchResult(raw) {
			aid := strings.TrimSpace(pyStr(it["album_id"]))
			if aid == "" {
				continue
			}
			img := strings.TrimSpace(pyStr(it["image"]))
			if img == "" {
				img = v2CoverURL(aid)
			}
			fallback = append(fallback, v2ComicSummary{
				Source:   "jm",
				ComicID:  aid,
				Title:    pyStr(it["title"]),
				Author:   v2StrPtr(authorText(it["author"])),
				CoverURL: v2StrPtr(img),
				Tags:     []string{},
				Raw:      it,
			})
		}
	}
	return v2PickUnique(rnd, fallback, v2RandomTarget), nil
}

func (jmProvider) AlsoViewed(ctx context.Context, identity, comicID string) (any, error) {
	ck := store.LoadCookies(identity)
	cur := strings.TrimSpace(comicID)
	seen := map[string]bool{}
	out := []v2ComicSummary{}
	add := func(m map[string]any) bool {
		aid := strings.TrimSpace(pyStr(m["id"]))
		if aid == "" || aid == cur || seen[aid] {
			return false
		}
		seen[aid] = true
		img := strings.TrimSpace(pyStr(m["image"]))
		if img == "" {
			img = v2CoverURL(aid)
		}
		out = append(out, v2ComicSummary{
			Source:   "jm",
			ComicID:  aid,
			Title:    pyStr(m["name"]),
			Author:   v2StrPtr(authorText(m["author"])),
			CoverURL: v2StrPtr(img),
			Tags:     []string{},
			Raw:      m,
		})
		return true
	}
	pres, perr := apiClient().Promote(ctx, "0", ck)
	if perr != nil {
		return nil, perr
	}
	if sections, isList := decodeAny(pres.Data).([]any); isList {
		for _, sec := range sections {
			sm := rawMap(sec)
			if sm == nil {
				continue
			}
			for _, c := range rawList(sm["content"]) {
				cm := rawMap(c)
				if cm == nil {
					continue
				}
				add(cm)
				if len(out) >= 24 {
					return out, nil
				}
			}
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	lres, lerr := apiClient().Latest(ctx, "0", ck)
	if lerr != nil {
		return nil, lerr
	}
	raw2 := decodeAny(lres.Data)
	if list, isList := raw2.([]any); isList {
		for _, x := range list {
			xm := rawMap(x)
			if xm == nil {
				continue
			}
			add(xm)
			if len(out) >= 24 {
				break
			}
		}
	} else {
		for _, it := range adaptSearchResult(raw2) {
			aid := strings.TrimSpace(pyStr(it["album_id"]))
			if aid == "" || aid == cur || seen[aid] {
				continue
			}
			seen[aid] = true
			img := strings.TrimSpace(pyStr(it["image"]))
			if img == "" {
				img = v2CoverURL(aid)
			}
			out = append(out, v2ComicSummary{
				Source:   "jm",
				ComicID:  aid,
				Title:    pyStr(it["title"]),
				Author:   v2StrPtr(authorText(it["author"])),
				CoverURL: v2StrPtr(img),
				Tags:     []string{},
				Raw:      it,
			})
			if len(out) >= 24 {
				break
			}
		}
	}
	return out, nil
}

func (jmProvider) ComicDetail(ctx context.Context, identity, comicID string) (any, error) {
	aq := url.Values{}
	aq.Set("comicName", "")
	aq.Set("id", comicID)
	res, err := apiClient().APIGet(ctx, "/album", aq, store.LoadCookies(identity))
	if err != nil {
		return nil, err
	}
	d := adaptAlbumDetail(decodeRawMap(res.Data))
	chapters := []v2ChapterSummary{}
	if eps, ok2 := d["episode_list"].([]map[string]any); ok2 {
		for idx, ep := range eps {
			cid := pyStr(ep["id"])
			if cid == "" {
				continue
			}
			chapters = append(chapters, v2ChapterSummary{ID: cid, Title: pyStr(ep["title"]), Order: idx})
		}
	}
	comicIDOut := pyStr(d["album_id"])
	if comicIDOut == "" {
		comicIDOut = comicID
	}
	tags, _ := d["tags"].([]string)
	if tags == nil {
		tags = []string{}
	}
	return v2ComicDetail{
		Source:      "jm",
		ComicID:     comicIDOut,
		Title:       pyStr(d["title"]),
		Author:      v2StrPtr(authorText(d["author"])),
		CoverURL:    v2StrPtr(pyStr(d["image"])),
		Description: v2StrPtr(pyStr(d["description"])),
		Tags:        tags,
		Category:    nil,
		IsFavorite:  store.JmIsFavorite(identity, comicIDOut),
		Chapters:    chapters,
		Raw:         d,
	}, nil
}

func (jmProvider) Chapter(ctx context.Context, identity, chapterID string) (any, error) {
	info, err := fetchJmChapter(ctx, store.LoadCookies(identity), chapterID)
	if err != nil {
		return nil, err
	}
	images := make([]v2ChapterPage, 0, len(info.Names))
	for _, s := range info.Names {
		if s == "" {
			continue
		}
		images = append(images, v2ChapterPage{Name: s})
	}
	return v2ChapterDetail{
		Source:    "jm",
		ChapterID: chapterID,
		Title:     v2StrPtr(info.Title),
		Images:    images,
		Raw:       chapterInfoMap(info, chapterID),
	}, nil
}

// Daily：JM 首页沿用既有的「连载更新 + 推荐」编排，不提供每日精选。
func (jmProvider) Daily(ctx context.Context, identity string) (any, error) {
	return nil, v2ProviderErr("该内容源暂不支持每日精选", 400)
}
