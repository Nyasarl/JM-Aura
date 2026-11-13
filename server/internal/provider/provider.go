package provider

import "context"

// Provider 是内容源的实现契约。
//
// 各方法返回的 any 为「已适配、可直接 JSON 序列化」的值（app 层的 v2 响应结构），
// 因此本包不依赖 app 层；反过来 app 层通过 provider.For(source) 取实现，
// 从而让同一套 handler 复用于不同内容源。
type Provider interface {
	// Source 返回源标识，须与注册时使用的 Source 常量一致。
	Source() Source

	// Caps 返回该源支持的能力集，供前端按源裁剪入口。
	Caps() Caps

	// —— 内容读取能力 ——
	Categories(ctx context.Context, identity string) (any, error)
	Search(ctx context.Context, identity string, keyword string, page int) (any, error)
	Leaderboard(ctx context.Context, identity string, q LeaderboardQuery) (any, error)

	// Random 返回随机页一次呈现的一批漫画（各源统一 10 本）。
	Random(ctx context.Context, identity string) (any, error)
	AlsoViewed(ctx context.Context, identity string, comicID string) (any, error)
	ComicDetail(ctx context.Context, identity string, comicID string) (any, error)
	Chapter(ctx context.Context, identity string, chapterID string) (any, error)

	// Daily 返回该源的「每日精选」首页内容：服务端每天生成一次并缓存，
	// 当天对所有用户一致，次日刷新。不支持的源返回 Not supported。
	Daily(ctx context.Context, identity string) (any, error)
}

// LeaderboardQuery 是各源榜单的统一入参，各源只使用其中与自己相关的那几个：
//
//	jm   ：Category（分类 id，0=全部）、Sort（tf/mv/mr）、Page、Tag
//	bika ：Category（榜单类型，目前只有 VC=浏览榜）、Period（H24/D7/D30）
//
// 之所以合并成一个结构体而不是继续加形参，是因为两个源的榜单维度本身不同：
// JM 是「分类 × 排序 × 分页」的组合，哔咔是「时间段 × 榜单类型」的定长列表。
// 用一个结构体承载，新增源时不会再把接口签名越拉越长。
type LeaderboardQuery struct {
	Category string
	Page     int
	Sort     string
	Tag      string
	Period   string
}
