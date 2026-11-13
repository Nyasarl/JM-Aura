package app

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"sync"
	"time"

	"jmaura/internal/bika"
	"jmaura/internal/provider"
	"jmaura/internal/store"
)

func init() {
	provider.Register(bikaProvider{})
}

// bikaProvider 通过 provider 契约接入哔咔源。
//
// 只读能力与 jm 同形输出；交互/账号类操作由 bikaauth.go 中的辅助函数承担，
// 由 v2 handler 按源分派（后续可继续上提到接口层）。
type bikaProvider struct{}

func (bikaProvider) Source() provider.Source { return provider.SourceBika }

func (bikaProvider) Caps() provider.Caps {
	return provider.Caps{
		Search:      true,
		Categories:  true,
		Leaderboard: true,
		Random:      true,
		ComicDetail: true,
		Chapter:     true,
		AlsoViewed:  true,
		Favorite:    true,
		Like:        true,
		Comment:     true,
		Checkin:     true,
		Download:    true,
		// 支持「一键注册」：上游 auth/register 可无邮箱验证直接建号（见 bikaauth.go）。
		Register: true,
		// 支持「每日精选」首页：服务端每天随机生成一次并缓存（见 daily_bika.go）。
		Daily: true,
	}
}

// bikaSort 把前端/JM 的排序语义映射为 bika 排序码：
// mr(最近更新)→dd、tf(最多爱心)→ld、mv(最多观看)→vd；原生 dd/da/ld/vd 原样透传。
func bikaSort(sort string) string {
	switch strings.TrimSpace(sort) {
	case "", "mr", "dd":
		return "dd"
	case "da":
		return "da"
	case "tf", "ld":
		return "ld"
	case "mv", "vd":
		return "vd"
	}
	return "dd"
}

func (bikaProvider) Categories(ctx context.Context, identity string) (any, error) {
	data, err := bikaCall(ctx, identity, func(t string) (any, error) {
		return bikaClient.Categories(ctx, t)
	})
	if err != nil {
		return nil, err
	}
	cats := bika.AsList(bika.AsMap(data)["categories"])
	out := make([]any, 0, len(cats))
	for _, c := range cats {
		m := bika.AsMap(c)
		if m == nil || bika.AsBool(m["isWeb"]) {
			continue
		}
		title := bika.AsStr(m["title"])
		if title == "" {
			continue
		}
		thumb := bika.AsMap(m["thumb"])
		out = append(out, map[string]any{
			"id":    title,
			"title": title,
			"name":  title,
			"cover": bikaProxyURL(bika.ImageURL(bika.AsStr(thumb["fileServer"]), bika.AsStr(thumb["path"]), bika.PictureElse)),
		})
	}
	return out, nil
}

func (bikaProvider) Search(ctx context.Context, identity, keyword string, page int) (any, error) {
	if page < 1 {
		page = 1
	}
	data, err := bikaCall(ctx, identity, func(t string) (any, error) {
		return bikaClient.AdvancedSearch(ctx, t, keyword, page, nil, "dd")
	})
	if err != nil {
		return nil, err
	}
	return bikaSummaries(data), nil
}

// bikaLeaderboardPeriods 是哔咔榜单支持的时间段，取自 App 端 LeaderboardTimeframe 枚举：
// H24=24小时、D7=7天、D30=30天。
var bikaLeaderboardPeriods = map[string]string{
	"H24": "24小时",
	"D7":  "7天",
	"D30": "30天",
}

// bikaLeaderboardPeriod 规范时间段：空值默认 24 小时，不认识的值直接报错，
// 而不是悄悄退回默认——否则前端传错参数时会看到「24小时」却以为在看别的榜单。
func bikaLeaderboardPeriod(v string) (string, error) {
	v = strings.ToUpper(strings.TrimSpace(v))
	if v == "" {
		return "H24", nil
	}
	if _, ok := bikaLeaderboardPeriods[v]; ok {
		return v, nil
	}
	return "", v2ProviderErr("不支持的榜单时间段: "+v+"（可用 H24/D7/D30）", 400)
}

// bikaLeaderboardCategory 规范榜单类型。App 端 LeaderboardCategory 目前只有
// VIEW_COUNT("VC","浏览榜") 这一项，所以前端传什么都按浏览榜处理；上游将来
// 若新增类型（例如按爱心/按收藏），在这里补映射即可，接口签名不用动。
func bikaLeaderboardCategory(string) string { return "VC" }

// Leaderboard 哔咔排行榜。
//
// 与 JM 的「分类 × 排序 × 分页」不同，哔咔上游是「时间段 × 榜单类型」的定长列表：
// GET /comics/leaderboard?tt={H24|D7|D30}&ct=VC，响应 data.comics[]，不分页。
// 因此这里忽略 page/sort/tag，只认 period 与 category。
func (bikaProvider) Leaderboard(ctx context.Context, identity string, q provider.LeaderboardQuery) (any, error) {
	period, err := bikaLeaderboardPeriod(q.Period)
	if err != nil {
		return nil, err
	}
	ct := bikaLeaderboardCategory(q.Category)
	key := period + ":" + ct

	items, fresh, ok := bikaLeaderboardLookup(key)
	if ok && fresh {
		return items, nil
	}
	if ok {
		// 过期但有旧值：先把旧值给用户，后台悄悄刷新。
		// 后台任务必须用独立 ctx——请求的 ctx 在响应结束时就取消了。
		go bikaLeaderboardRefresh(key, period, ct, identity)
		return items, nil
	}

	// 冷启动：同步取。并发请求由 single-flight 合并。
	return bikaLeaderboardFetch(ctx, key, period, ct, identity)
}

// bikaLeaderboardFetch 同步取榜单，同一 key 的并发调用只会打一次上游。
func bikaLeaderboardFetch(ctx context.Context, key, period, ct, identity string) ([]v2ComicSummary, error) {
	bikaLeaderboardMu.Lock()
	if f, running := bikaLeaderboardFly[key]; running {
		bikaLeaderboardMu.Unlock()
		select {
		case <-f.done:
			if f.err != nil {
				return nil, f.err
			}
			return copyBikaSummaries(f.items), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := &bikaLeaderboardFlight{done: make(chan struct{})}
	bikaLeaderboardFly[key] = f
	bikaLeaderboardMu.Unlock()

	items, err := bikaLeaderboardLoad(ctx, period, ct, identity)

	bikaLeaderboardMu.Lock()
	if err == nil {
		bikaLeaderboardTable[key] = bikaLeaderboardEntry{
			items: copyBikaSummaries(items),
			exp:   time.Now().Add(bikaLeaderboardTTL),
		}
	}
	f.items, f.err = items, err
	close(f.done)
	// 放在最后删除：close 与 delete 之间到达的请求会直接复用这个刚完成的结果。
	if cur, still := bikaLeaderboardFly[key]; still && cur == f {
		delete(bikaLeaderboardFly, key)
	}
	bikaLeaderboardMu.Unlock()

	if err != nil {
		return nil, err
	}
	return copyBikaSummaries(items), nil
}

// bikaLeaderboardRefresh 后台刷新（stale-while-revalidate）。已有刷新在跑时直接跳过，
// 避免过期瞬间涌进来的每个请求都各起一个 goroutine。
func bikaLeaderboardRefresh(key, period, ct, identity string) {
	bikaLeaderboardMu.Lock()
	if _, running := bikaLeaderboardFly[key]; running {
		bikaLeaderboardMu.Unlock()
		return
	}
	bikaLeaderboardMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// 刷新失败不影响任何人（旧值仍在缓存里，下次请求会再试一次）。
	_, _ = bikaLeaderboardFetch(ctx, key, period, ct, identity)
}

// bikaLeaderboardLoad 真正打上游一次。
func bikaLeaderboardLoad(ctx context.Context, period, ct, identity string) ([]v2ComicSummary, error) {
	data, err := bikaCall(ctx, identity, func(t string) (any, error) {
		return bikaClient.Leaderboard(ctx, t, period, ct)
	})
	if err != nil {
		return nil, err
	}
	return bikaSummaries(data), nil
}

// 榜单缓存：榜单是「按时间段统计」的慢变量（App 端也缓存 15 分钟），这里取 10 分钟。
//
// 三层保护，目标是「用户永远不需要等上游」：
//  1. 命中且新鲜 → 直接返回（实测上游往返 ~226ms，命中 ~25ms）；
//  2. 已过期但还有旧值 → 立即返回旧值，后台刷新（stale-while-revalidate），
//     所以即使正好卡在过期瞬间，用户也不会看到延迟；
//  3. 完全没有值（冷启动 / 刚换时间段）→ 同步取一次，并用 single-flight 合并
//     瞬时并发：N 个用户同时打开榜单页只会打出 1 个上游请求。
const bikaLeaderboardTTL = 10 * time.Minute

type bikaLeaderboardEntry struct {
	items []v2ComicSummary
	exp   time.Time
}

// bikaLeaderboardFlight 是一次进行中的上游请求，供并发请求共享结果。
type bikaLeaderboardFlight struct {
	done  chan struct{}
	items []v2ComicSummary
	err   error
}

var (
	bikaLeaderboardMu    sync.Mutex
	bikaLeaderboardTable = map[string]bikaLeaderboardEntry{}
	bikaLeaderboardFly   = map[string]*bikaLeaderboardFlight{}
)

// copyBikaSummaries 复制切片。必须复制：handler 里的 v2MaskList 用 items[:0] 原地
// 压缩来过滤条目，直接返回缓存切片会把缓存内容改写掉。
func copyBikaSummaries(in []v2ComicSummary) []v2ComicSummary {
	if len(in) == 0 {
		return in
	}
	out := make([]v2ComicSummary, len(in))
	copy(out, in)
	return out
}

// bikaLeaderboardLookup 返回缓存条目。fresh 为 false 表示条目已过期但仍可用作
// stale 兜底；ok 为 false 表示完全没有缓存。
func bikaLeaderboardLookup(key string) (items []v2ComicSummary, fresh, ok bool) {
	bikaLeaderboardMu.Lock()
	e, found := bikaLeaderboardTable[key]
	bikaLeaderboardMu.Unlock()
	if !found || len(e.items) == 0 {
		return nil, false, false
	}
	return copyBikaSummaries(e.items), time.Now().Before(e.exp), true
}

// —— 随机 / 每日精选 共用的抽取内核 ——
//
// 哔咔上游没有「一次给多本随机」的接口：/comics/random 每次只回 1 本。
// 因此这里用「随机分类列表页 + 上游纯随机单抽」并发组合来近似「有选择性的随机」：
// 列表页负责覆盖面，单抽负责制造偶遇。结果按抽取次序收集，给定种子即可复现。

var bikaListSorts = []string{"dd", "da", "ld", "vd"}

const (
	bikaRandomListDraws = 3 // 随机（排序 × 分类 × 页码）列表抽取次数
	bikaRandomSingles   = 4 // 上游纯随机单抽次数
)

// Random 返回随机页一次呈现的 10 本（每次调用重新随机）。
func (bikaProvider) Random(ctx context.Context, identity string) (any, error) {
	seed := time.Now().UnixNano()
	pool, err := bikaDrawPoolAuth(ctx, identity, false, bikaRandomListDraws, bikaRandomSingles, seed)
	if err != nil {
		return nil, err
	}
	return v2PickUnique(rand.New(rand.NewSource(seed)), pool, v2RandomTarget), nil
}

// bikaPreferredToken 解析首选 token：preferPublic 时优先公共账号（每日精选要求
// 所有用户看到同一份内容），否则按请求身份解析。
func bikaPreferredToken(ctx context.Context, identity string, preferPublic bool) string {
	if preferPublic {
		if t := bikaPublicTokenGet(ctx, false); t != "" {
			return t
		}
	}
	return bikaTokenFor(ctx, identity)
}

// bikaDrawPoolAuth 跑一次多抽，并在登录失效时刷新 token 重试一次。
//
// 这是「公共账号 / 用户 token 过期后自动重登」的关键：每日精选与随机页都走这里，
// 不再直连上游，因此不会再出现「掉登录却不重新登录」。
func bikaDrawPoolAuth(ctx context.Context, identity string, preferPublic bool, listDraws, singles int, seed int64) ([]v2ComicSummary, error) {
	token := bikaPreferredToken(ctx, identity, preferPublic)
	if token == "" {
		return nil, bika.ErrNeedLogin
	}
	pool, err := bikaDrawPool(ctx, token, listDraws, singles, seed)
	if err == nil {
		return pool, nil
	}
	if !errors.Is(err, bika.ErrNeedLogin) {
		return nil, err
	}
	// 刷新后重试；沿用同一个 seed，保证每日精选即使走了重试也仍是同一份内容。
	var refreshed string
	if preferPublic {
		refreshed = bikaPublicTokenGet(ctx, true)
		if refreshed == "" {
			refreshed = bikaRefreshToken(ctx, identity)
		}
	} else {
		refreshed = bikaRefreshToken(ctx, identity)
	}
	if refreshed == "" || refreshed == token {
		return nil, err
	}
	return bikaDrawPool(ctx, refreshed, listDraws, singles, seed)
}

// bikaCategoryTitles 取上游分类名（过滤 Web 端专用分类），供随机挑选。
func bikaCategoryTitles(ctx context.Context, token string) []string {
	data, err := bikaClient.Categories(ctx, token)
	if err != nil {
		return nil
	}
	cats := bika.AsList(bika.AsMap(data)["categories"])
	out := make([]string, 0, len(cats))
	for _, c := range cats {
		m := bika.AsMap(c)
		if m == nil || bika.AsBool(m["isWeb"]) {
			continue
		}
		if t := strings.TrimSpace(bika.AsStr(m["title"])); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// bikaDrawPool 并发执行若干次列表抽取与纯随机单抽，合并为一个候选池（未去重）。
// seed 同时决定抽取参数，保证给定同一个 seed 时输出稳定（供每日精选当天复现）。
func bikaDrawPool(ctx context.Context, token string, listDraws, singles int, seed int64) ([]v2ComicSummary, error) {
	cats := bikaCategoryTitles(ctx, token)
	rnd := rand.New(rand.NewSource(seed))

	type draw struct {
		cat  string
		sort string
		page int
		rand bool
	}
	draws := make([]draw, 0, listDraws+singles)
	for i := 0; i < listDraws; i++ {
		c := ""
		if n := len(cats); n > 0 {
			c = cats[rnd.Intn(n)]
		}
		draws = append(draws, draw{
			cat:  c,
			sort: bikaListSorts[rnd.Intn(len(bikaListSorts))],
			page: 1 + rnd.Intn(6),
		})
	}
	for i := 0; i < singles; i++ {
		draws = append(draws, draw{rand: true})
	}

	results := make([][]v2ComicSummary, len(draws))
	errs := make([]error, len(draws))
	var wg sync.WaitGroup
	for i, d := range draws {
		wg.Add(1)
		go func(i int, d draw) {
			defer wg.Done()
			if d.rand {
				data, err := bikaClient.Random(ctx, token)
				if err != nil {
					errs[i] = err
					return
				}
				results[i] = bikaSummaries(data)
				return
			}
			data, err := bikaClient.ComicsByCategory(ctx, token, d.cat, d.sort, d.page)
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = bikaSummaries(data)
		}(i, d)
	}
	wg.Wait()

	var (
		pool      []v2ComicSummary
		first     error
		needLogin bool
	)
	for i := range results {
		pool = append(pool, results[i]...)
		if errs[i] == nil {
			continue
		}
		// 优先把「登录失效」透出去，好让上层触发一次重登重试。
		if errors.Is(errs[i], bika.ErrNeedLogin) {
			needLogin = true
		} else if first == nil {
			first = errs[i]
		}
	}
	if len(pool) == 0 {
		if needLogin {
			return nil, bika.ErrNeedLogin
		}
		if first != nil {
			return nil, first
		}
		return nil, v2ProviderErr("暂时没有可获取的内容", 400)
	}
	return pool, nil
}

func (bikaProvider) AlsoViewed(ctx context.Context, identity, comicID string) (any, error) {
	data, err := bikaCall(ctx, identity, func(t string) (any, error) {
		return bikaClient.ComicRecommend(ctx, t, comicID)
	})
	if err != nil {
		return nil, err
	}
	items := bikaSummaries(data)
	out := make([]v2ComicSummary, 0, 24)
	for _, it := range items {
		if it.ComicID == comicID {
			continue
		}
		out = append(out, it)
		if len(out) >= 24 {
			break
		}
	}
	return out, nil
}

func (bikaProvider) ComicDetail(ctx context.Context, identity, comicID string) (any, error) {
	return bikaComicDetailData(ctx, identity, comicID)
}

func (bikaProvider) Chapter(ctx context.Context, identity, chapterID string) (any, error) {
	return bikaChapterDetailData(ctx, identity, chapterID)
}

// —— 交互 / 账号（由 v2 handler 按源分派调用） ——

// isBikaSource 供其他文件按源分派时复用（避免各处重复字符串比较）。
func isBikaSource(s string) bool { return s == string(provider.SourceBika) }

// bikaFavoriteList 云端收藏列表（统一形态 {items, folders, pages}）。
// 收藏属于用户私有数据：必须用户本人已登录，绝不用公共账号代读（否则会串到公共账号的收藏）。
func bikaFavoriteList(ctx context.Context, identity string, page int) (any, error) {
	if err := bikaRequireUser(identity); err != nil {
		return nil, err
	}
	if page < 1 {
		page = 1
	}
	data, err := bikaCall(ctx, identity, func(t string) (any, error) {
		return bikaClient.Favorites(ctx, t, "dd", page)
	})
	if err != nil {
		return nil, err
	}
	// 收藏缩略图必须用 favourite 图片策略（storage1 → s3.picacomic.com）。
	items := bikaSummariesWith(data, bika.PictureFavorite)
	pages := page
	if m := bika.AsMap(bika.AsMap(data)["comics"]); m != nil {
		if p := bika.AsInt(m["pages"]); p > 0 {
			pages = p
		}
	}
	return map[string]any{"items": items, "folders": []any{}, "pages": pages}, nil
}

// bikaIsFavorite 轻量查询某漫画的收藏态（只拉 detail，不含章节）。
// 未登录时恒为 false —— 公共账号的收藏态不代表当前用户，绝不能透出。
func bikaIsFavorite(ctx context.Context, identity, comicID string) bool {
	if !bikaUserLoggedIn(identity) {
		return false
	}
	data, err := bikaCall(ctx, identity, func(t string) (any, error) {
		return bikaClient.ComicDetail(ctx, t, comicID)
	})
	if err != nil {
		return false
	}
	comic := bika.AsMap(bika.AsMap(data)["comic"])
	if comic == nil {
		comic = bika.AsMap(data)
	}
	return bika.AsBool(comic["isFavourite"])
}

// bikaToggleFavorite 切换收藏（写操作，要求用户本人已登录）。
func bikaToggleFavorite(ctx context.Context, identity, comicID string) (any, error) {
	if err := bikaRequireUser(identity); err != nil {
		return nil, err
	}
	data, err := bikaCall(ctx, identity, func(t string) (any, error) {
		return bikaClient.ToggleFavorite(ctx, t, comicID)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"result":      data,
		"is_favorite": bikaIsFavorite(ctx, identity, comicID),
	}, nil
}

// bikaToggleLike 切换喜欢（写操作）。
func bikaToggleLike(ctx context.Context, identity, comicID string) (any, error) {
	if err := bikaRequireUser(identity); err != nil {
		return nil, err
	}
	data, err := bikaCall(ctx, identity, func(t string) (any, error) {
		return bikaClient.ToggleLike(ctx, t, comicID)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": data}, nil
}

// bikaProfileData 用户本人资料（未登录返回未登录错误，绝不下发公共账号）。
func bikaProfileData(ctx context.Context, identity string) (any, error) {
	if !bikaUserLoggedIn(identity) {
		return nil, v2ProviderErr("请先登录哔咔账号", 401)
	}
	siteUser, _ := bikaSiteUserOf(identity)
	data, err := bikaCall(ctx, identity, func(t string) (any, error) {
		return bikaClient.Profile(ctx, t)
	})
	if err != nil {
		return nil, err
	}
	user := bika.AsMap(bika.AsMap(data)["user"])
	avatar := bika.AsMap(user["avatar"])
	prof := v2UserProfile{
		Source:    bikaSource,
		Nickname:  v2StrPtr(bika.AsStr(user["name"])),
		Signature: v2StrPtr(bika.AsStr(user["slogan"])),
		Raw:       user,
	}
	if prof.Nickname != nil && *prof.Nickname == "" {
		prof.Nickname = nil
	}
	if prof.Signature != nil && *prof.Signature == "" {
		prof.Signature = nil
	}
	if u := bika.AsStr(user["email"]); u != "" {
		prof.Username = v2StrPtr(u)
	} else if u := store.BikaCredActive(siteUser); u != "" {
		prof.Username = v2StrPtr(u)
	}
	if av := bikaProxyURL(bika.ImageURL(bika.AsStr(avatar["fileServer"]), bika.AsStr(avatar["path"]), bika.PictureCreator)); av != "" {
		prof.AvatarURL = v2StrPtr(av)
	}
	return prof, nil
}

// bikaCheckin 账号签到（写操作）。
func bikaCheckin(ctx context.Context, identity string) (any, error) {
	if err := bikaRequireUser(identity); err != nil {
		return nil, err
	}
	data, err := bikaCall(ctx, identity, func(t string) (any, error) {
		return bikaClient.PunchIn(ctx, t)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": data}, nil
}

// —— 评论 ——

// bikaCommentNode 把 bika 评论项映射为前端 V2Comment 兼容形态。
func bikaCommentNode(item map[string]any) map[string]any {
	user := bika.AsMap(item["_user"])
	avatar := bika.AsMap(user["avatar"])
	replyCount := bika.AsInt(item["commentsCount"])
	if replyCount == 0 {
		replyCount = bika.AsInt(item["totalComments"])
	}
	return map[string]any{
		"CID":         bikaComicID(item),
		"nickname":    bika.AsStr(user["name"]),
		"username":    bika.AsStr(user["name"]),
		"avatar":      bikaProxyURL(bika.ImageURL(bika.AsStr(avatar["fileServer"]), bika.AsStr(avatar["path"]), bika.PictureCreator)),
		"content":     bikaStripHTML(bika.AsStr(item["content"])),
		"created_at":  bika.AsStr(item["created_at"]),
		"likes":       bika.AsInt(item["likesCount"]),
		"reply_count": replyCount,
		"replys":      []any{},
	}
}

// bikaComments 评论流（只读公开内容，公共账号可读）。
// bika 的子评论是懒加载的，这里对有回复的评论并发拉取首页子评论嵌入 replys，
// 以便复用现有「楼中楼」渲染（并发上限 6，避免打爆上游）。
func bikaComments(ctx context.Context, identity, comicID string, page int) (any, error) {
	if page < 1 {
		page = 1
	}
	data, err := bikaCall(ctx, identity, func(t string) (any, error) {
		return bikaClient.Comments(ctx, t, comicID, page)
	})
	if err != nil {
		return nil, err
	}
	dm := bika.AsMap(data)
	cm := bika.AsMap(dm["comments"])
	docs := bika.AsList(cm["docs"])
	list := make([]any, 0, len(docs))
	for _, d := range docs {
		m := bika.AsMap(d)
		if m == nil {
			continue
		}
		list = append(list, bikaCommentNode(m))
	}
	total := bika.AsInt(cm["total"])
	if total == 0 {
		total = len(list)
	}

	type job struct {
		idx int
		cid string
	}
	jobs := make([]job, 0, len(list))
	for i, c := range list {
		node := c.(map[string]any)
		if bika.AsInt(node["reply_count"]) > 0 {
			if cid := bika.AsStr(node["CID"]); cid != "" {
				jobs = append(jobs, job{idx: i, cid: cid})
			}
		}
	}
	if len(jobs) > 0 {
		var wg sync.WaitGroup
		sem := make(chan struct{}, 6)
		for _, j := range jobs {
			wg.Add(1)
			go func(j job) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
				defer cancel()
				kres, kerr := bikaCall(cctx, identity, func(t string) (any, error) {
					return bikaClient.CommentChildren(cctx, t, j.cid, 1)
				})
				if kerr != nil {
					return
				}
				kids := bika.AsList(bika.AsMap(bika.AsMap(kres)["comments"])["docs"])
				replies := make([]any, 0, len(kids))
				for _, k := range kids {
					if km := bika.AsMap(k); km != nil {
						replies = append(replies, bikaCommentNode(km))
					}
				}
				list[j.idx].(map[string]any)["replys"] = replies
			}(j)
		}
		wg.Wait()
	}
	return map[string]any{"list": list, "total": total}, nil
}

// bikaSendComment 发表评论 / 回复（写操作，要求用户本人已登录）。
func bikaSendComment(ctx context.Context, identity, comicID, content, replyTo string) (any, error) {
	if err := bikaRequireUser(identity); err != nil {
		return nil, err
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, v2ProviderErr("评论内容不能为空", 400)
	}
	var out any
	var err error
	if replyTo != "" {
		out, err = bikaCall(ctx, identity, func(t string) (any, error) {
			return bikaClient.PostReply(ctx, t, replyTo, content)
		})
	} else {
		out, err = bikaCall(ctx, identity, func(t string) (any, error) {
			return bikaClient.PostComment(ctx, t, comicID, content)
		})
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": out}, nil
}

// bikaLikeComment 哔咔上游不提供评论点赞。
func bikaLikeComment() (any, error) {
	return nil, v2ProviderErr("哔咔暂不支持评论点赞", 400)
}
