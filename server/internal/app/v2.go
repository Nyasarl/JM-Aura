package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"jmaura/internal/jm"
	"jmaura/internal/provider"
	"jmaura/internal/store"
)

type v2ProviderError struct {
	msg    string
	status int
}

func (e *v2ProviderError) Error() string { return e.msg }

func v2ProviderErr(msg string, status int) *v2ProviderError {
	return &v2ProviderError{msg: msg, status: status}
}

// v2Fail mirrors backend._v2_err: NeedLogin-ish failures map to UserError,
// ProviderError(401) maps to UserError, everything else maps to Error.
// All responses keep HTTP 200 like the Python implementation.
func v2Fail(w http.ResponseWriter, e error) {
	var pe *v2ProviderError
	if errors.As(e, &pe) {
		st := StatusError
		switch pe.status {
		case 401:
			st = StatusUserError
		case 429:
			// 下载/导出排队已满：下载子系统繁忙，而非参数错误。
			st = StatusDownloadBusy
		}
		writeJSON(w, 200, errSt(st, pe.msg))
		return
	}
	var ue *provider.UnsupportedError
	if errors.As(e, &ue) {
		writeJSON(w, 200, errSt(StatusError, ue.Error()))
		return
	}
	if isBikaNeedLogin(e) {
		writeJSON(w, 200, errSt(StatusUserError, "哔咔登录已过期，请重新登录"))
		return
	}
	if is401Err(e) {
		writeJSON(w, 200, errSt(StatusUserError, e.Error()))
		return
	}
	writeJSON(w, 200, errSt(StatusError, apiErrMsg(e)))
}

func v2OK(w http.ResponseWriter, data any) {
	writeJSON(w, 200, ok(data, ""))
}

// v2GateBlocked replicates the FastAPI gate used on auth/login + auth/register:
// the source comparison lower-cases, while get_provider later does not.
func v2GateBlocked(w http.ResponseWriter, r *http.Request, source string) bool {
	if strings.ToLower(source) != "jm" || getSiteUser(r) != "" {
		return false
	}
	writeJSON(w, 401, errSt(StatusNotLogin, "Aura login required"))
	return true
}

func v2ProviderCheck(source string) error {
	if source == "jm" {
		return nil
	}
	return v2ProviderErr(fmt.Sprintf("Not supported source: %s", source), 400)
}

// jmRegisterURL 返回「前往禁漫官网注册」按钮应指向的地址。
// 默认用官方注册页；可用 JM_AURA_JM_WEB_BASE 覆盖，域名轮换时无需重编前端。
func jmRegisterURL() string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("JM_AURA_JM_WEB_BASE")), "/")
	if base == "" {
		base = "https://18comic.ink"
	}
	if strings.HasSuffix(base, "/signup") {
		return base
	}
	return base + "/signup"
}

// handleV2Capabilities 暴露某源的能力集，供前端按源裁剪入口、判断登录态。
func handleV2Capabilities(w http.ResponseWriter, r *http.Request) {
	p, err := provider.For(r.PathValue("source"))
	if err != nil {
		v2Fail(w, err)
		return
	}
	loggedIn := false
	if p.Source() == provider.SourceBika {
		loggedIn = bikaUserLoggedIn(effIdentityFor(r, string(provider.SourceBika)))
	} else if siteU := getSiteUser(r); siteU != "" {
		loggedIn = store.CredActiveUsername(siteU) != ""
	}
	payload := map[string]any{
		"source":         string(p.Source()),
		"caps":           p.Caps(),
		"logged_in":      loggedIn,
		"public_account": p.Source() == provider.SourceBika && BikaPublicConfigured(),
	}
	if p.Source() == provider.SourceJM {
		// jm 的注册改为「前往官网」：前端用这个地址渲染按钮，域名轮换时无需重编前端。
		payload["register_url"] = jmRegisterURL()
	}
	v2OK(w, payload)
}

// handleV2Favorites 按源返回云端收藏，统一形态 {items, folders, pages}。
func handleV2Favorites(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("source") == string(provider.SourceBika) {
		page, pok := v2QueryInt(w, r, "page", 1)
		if !pok {
			return
		}
		data, berr := bikaFavoriteList(r.Context(), effIdentityFor(r, string(provider.SourceBika)), page)
		if berr != nil {
			v2Fail(w, berr)
			return
		}
		v2OK(w, data)
		return
	}
	page := atoiDefault(r.URL.Query().Get("page"), 1)
	fid := r.URL.Query().Get("folder_id")
	if fid == "" {
		fid = "0"
	}
	identity := effIdentityOf(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, err := apiClient().APIGet(ctx, "/favorite", favoriteQuery(page, fid), store.LoadCookies(identity))
	if err != nil && is401Err(err) && reloginFromSavedConfig(r) {
		res, err = apiClient().APIGet(ctx, "/favorite", favoriteQuery(page, fid), store.LoadCookies(identity))
	}
	if err != nil {
		v2Fail(w, err)
		return
	}
	data := adaptFavorites(decodeAny(res.Data))
	items := []v2ComicSummary{}
	if content, okc := data["content"].([]map[string]any); okc {
		for _, it := range content {
			if pyStr(it["album_id"]) == "" {
				continue
			}
			items = append(items, v2SummaryFromAdapt(it, false))
		}
	}
	folders := data["folders"]
	if folders == nil {
		folders = []any{}
	}
	v2OK(w, map[string]any{"items": items, "folders": folders, "pages": data["pages"]})
}

// handleV2Logout 按源注销：jm 清上游会话与缓冲，bika 清本地绑定的凭据。
func handleV2Logout(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("source") == string(provider.SourceBika) {
		if siteU := getSiteUser(r); siteU != "" {
			_ = bikaUserLogout(siteU)
		}
		v2OK(w, map[string]any{"ok": true})
		return
	}
	identity := effIdentityOf(r)
	_ = store.ClearCookies(identity)
	store.JmSetUserID(identity, "")
	store.JmSetProfile(identity, map[string]any{})
	clearOpYmlCredsAt(opYmlPath())
	v2OK(w, map[string]any{"ok": true})
}

// v2DecodeBody emulates FastAPI body validation happening before the handler.
func v2DecodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpDetail(w, 422, err.Error())
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		httpDetail(w, 422, "Field required")
		return false
	}
	if uerr := json.Unmarshal(body, dst); uerr != nil {
		httpDetail(w, 422, uerr.Error())
		return false
	}
	return true
}

func v2RequireFields(w http.ResponseWriter, fields ...*string) bool {
	for _, f := range fields {
		if f == nil {
			httpDetail(w, 422, "Field required")
			return false
		}
	}
	return true
}

func v2QueryInt(w http.ResponseWriter, r *http.Request, name string, def int) (int, bool) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return def, true
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		httpDetail(w, 422, "Input should be a valid integer, unable to parse string as an integer")
		return 0, false
	}
	return n, true
}

type v2AuthRequest struct {
	Username *string `json:"username"`
	Password *string `json:"password"`
}

type v2RegisterRequest struct {
	Username *string `json:"username"`
	Password *string `json:"password"`
	Name     *string `json:"name"`
	Gender   *string `json:"gender"`
	Birthday *string `json:"birthday"`
}

type v2UpdateProfileRequest struct {
	Signature *string `json:"signature"`
}

type v2UpdatePasswordRequest struct {
	OldPassword *string `json:"old_password"`
	NewPassword *string `json:"new_password"`
}

type v2SendCommentRequest struct {
	Content *string `json:"content"`
	ReplyTo *string `json:"reply_to"`
}

type v2DownloadTaskRequest struct {
	ComicID    *string             `json:"comic_id"`
	ComicTitle *string             `json:"comic_title"`
	Chapters   []map[string]string `json:"chapters"`
	IncludeAll bool                `json:"include_all"`
	// Lossless=true 时以无损 PNG 打包（100% 无损、体积大）；默认 false 用 JPEG（体积小）。
	Lossless bool `json:"lossless"`
}

// Response shapes follow models/schemas.py field order exactly; pointer
// fields serialize as null, slices always serialize as arrays.
type v2UserProfile struct {
	Source    string         `json:"source"`
	Username  *string        `json:"username"`
	Nickname  *string        `json:"nickname"`
	AvatarURL *string        `json:"avatar_url"`
	Signature *string        `json:"signature"`
	Raw       map[string]any `json:"raw"`
}

type v2ComicSummary struct {
	Source   string         `json:"source"`
	ComicID  string         `json:"comic_id"`
	Title    string         `json:"title"`
	Author   *string        `json:"author"`
	CoverURL *string        `json:"cover_url"`
	Tags     []string       `json:"tags"`
	Category *string        `json:"category"`
	Raw      map[string]any `json:"raw"`
}

type v2ChapterSummary struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Order int    `json:"order"`
}

type v2ComicDetail struct {
	Source      string             `json:"source"`
	ComicID     string             `json:"comic_id"`
	Title       string             `json:"title"`
	Author      *string            `json:"author"`
	CoverURL    *string            `json:"cover_url"`
	Description *string            `json:"description"`
	Tags        []string           `json:"tags"`
	Category    *string            `json:"category"`
	IsFavorite  bool               `json:"is_favorite"`
	Chapters    []v2ChapterSummary `json:"chapters"`
	// 命中屏蔽规则时置位，供详情页提示/遮挡；未命中时不出现（omitempty）。
	Masked       bool           `json:"masked,omitempty"`
	MaskedReason string         `json:"masked_reason,omitempty"`
	Raw          map[string]any `json:"raw"`
}

type v2ChapterPage struct {
	Name string  `json:"name"`
	URL  *string `json:"url"`
}

type v2ChapterDetail struct {
	Source    string          `json:"source"`
	ChapterID string          `json:"chapter_id"`
	Title     *string         `json:"title"`
	Images    []v2ChapterPage `json:"images"`
	Raw       map[string]any  `json:"raw"`
}

func v2StrPtr(s string) *string { return &s }

func v2CoverURL(aid string) string {
	base := imgBase()
	if base == "" {
		return ""
	}
	return base + "/media/albums/" + aid + ".jpg"
}

func v2SummaryFromAdapt(it map[string]any, withCategory bool) v2ComicSummary {
	s := v2ComicSummary{
		Source:   "jm",
		ComicID:  pyStr(it["album_id"]),
		Title:    pyStr(it["title"]),
		Author:   v2StrPtr(authorText(it["author"])),
		CoverURL: v2StrPtr(pyStr(it["image"])),
		Tags:     []string{},
		Raw:      it,
	}
	if withCategory {
		s.Category = v2StrPtr(pyStr(it["category"]))
	}
	return s
}

func v2SummaryFromLatestItem(m map[string]any) (v2ComicSummary, bool) {
	if !pyTruthy(m["id"]) {
		return v2ComicSummary{}, false
	}
	aid := strings.TrimSpace(pyStr(m["id"]))
	if aid == "" {
		return v2ComicSummary{}, false
	}
	img := strings.TrimSpace(pyStr(m["image"]))
	if img == "" {
		img = v2CoverURL(aid)
	}
	return v2ComicSummary{
		Source:   "jm",
		ComicID:  aid,
		Title:    pyStr(m["name"]),
		Author:   v2StrPtr(authorText(m["author"])),
		CoverURL: v2StrPtr(img),
		Tags:     []string{},
		Raw:      m,
	}, true
}

func v2CatID(c any) string {
	switch t := c.(type) {
	case nil:
		return "0"
	case bool:
		if !t {
			return "0"
		}
		return "True"
	case string:
		if t == "" {
			return "0"
		}
		return t
	case json.Number:
		return t.String()
	case map[string]any:
		slug := t["slug"]
		if !pyTruthy(slug) {
			slug = t["SLUG"]
		}
		if pyTruthy(slug) {
			return pyStr(slug)
		}
		v := t["CID"]
		if !pyTruthy(v) {
			v = t["id"]
		}
		if !pyTruthy(v) {
			v = t["category_id"]
		}
		if !pyTruthy(v) {
			v = t["cid"]
		}
		if !pyTruthy(v) {
			return "0"
		}
		return pyStr(v)
	default:
		return "0"
	}
}

func v2FetchCategoryIDs(ctx context.Context, ck map[string]string) []string {
	out := []string{"0"}
	res, err := apiClient().Categories(ctx, ck)
	if err != nil {
		return out
	}
	m := rawMap(decodeAny(res.Data))
	if m == nil {
		return out
	}
	cats := rawList(m["categories"])
	if len(cats) == 0 {
		cats = rawList(m["data"])
	}
	ids := make([]string, 0, len(cats))
	for _, c := range cats {
		id := v2CatID(c)
		if id != "" && id != "None" {
			ids = append(ids, id)
		}
	}
	all := append([]string{"0"}, ids...)
	seen := make(map[string]bool, len(all))
	final := make([]string, 0, len(all))
	for _, id := range all {
		if seen[id] {
			continue
		}
		seen[id] = true
		final = append(final, id)
	}
	return final
}

// v2LeaderboardItems reproduces GetSearchCategoryReq2 emissions plus the
// provider-side summary mapping (no category field).
func v2LeaderboardItems(ctx context.Context, ck map[string]string, category string, page int, sort, tag string) ([]v2ComicSummary, error) {
	q := url.Values{}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if sort != "" {
		q.Set("o", sort)
	}
	if category != "" {
		q.Set("c", category)
	}
	if tag != "" {
		q.Set("t", tag)
	}
	res, err := apiClient().APIGet(ctx, "/categories/filter", q, ck)
	if err != nil {
		return nil, err
	}
	items := adaptSearchResult(decodeAny(res.Data))
	out := make([]v2ComicSummary, 0, len(items))
	for _, it := range items {
		if pyStr(it["album_id"]) == "" {
			continue
		}
		out = append(out, v2SummaryFromAdapt(it, false))
	}
	return out, nil
}

func handleV2Login(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	var req v2AuthRequest
	if !v2DecodeBody(w, r, &req) {
		return
	}
	if !v2RequireFields(w, req.Username, req.Password) {
		return
	}
	if v2GateBlocked(w, r, source) {
		return
	}
	if source == string(provider.SourceBika) {
		siteU := getSiteUser(r)
		if siteU == "" {
			v2Fail(w, v2ProviderErr("请先登录站点账号", 401))
			return
		}
		data, berr := bikaUserLogin(r.Context(), siteU, *req.Username, *req.Password)
		if berr != nil {
			v2Fail(w, berr)
			return
		}
		v2OK(w, data)
		return
	}
	if perr := v2ProviderCheck(source); perr != nil {
		v2Fail(w, perr)
		return
	}
	_, captured, raw, lerr := jm.NewClient().Login(r.Context(), *req.Username, *req.Password)
	if lerr != nil {
		v2Fail(w, lerr)
		return
	}
	identity := effIdentityOf(r)
	_ = store.SaveCookies(identity, captured)
	if siteU := getSiteUser(r); siteU != "" {
		_ = store.CredSet(siteU, *req.Username, *req.Password)
	}
	if raw != nil {
		store.JmSetProfile(identity, raw)
		for _, k := range []string{"uid", "user_id", "id"} {
			if v := raw[k]; v != nil && pyTruthy(v) {
				store.JmSetUserID(identity, pyStr(v))
				break
			}
		}
	}
	if raw == nil {
		raw = map[string]any{}
	}
	v2OK(w, raw)
}

func handleV2Register(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("source") == string(provider.SourceBika) {
		// 一键注册：立即返回后台任务快照，重活交给后端，前端只轮询进度（幂等，连点不会重复建号）。
		siteU := getSiteUser(r)
		if siteU == "" {
			v2Fail(w, v2ProviderErr("请先登录站点账号", 401))
			return
		}
		v2OK(w, bikaRegisterStart(siteU))
		return
	}
	source := r.PathValue("source")
	var req v2RegisterRequest
	if !v2DecodeBody(w, r, &req) {
		return
	}
	if !v2RequireFields(w, req.Username, req.Password) {
		return
	}
	if v2GateBlocked(w, r, source) {
		return
	}
	if perr := v2ProviderCheck(source); perr != nil {
		v2Fail(w, perr)
		return
	}
	v2Fail(w, v2ProviderErr("JM register not supported in app API", 400))
}

// handleV2RegisterStatus 查询一键注册后台任务的进度（仅任务归属者可查）。
func handleV2RegisterStatus(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("source") != string(provider.SourceBika) {
		v2Fail(w, v2ProviderErr("Not supported source", 400))
		return
	}
	siteU := getSiteUser(r)
	if siteU == "" {
		v2Fail(w, v2ProviderErr("请先登录站点账号", 401))
		return
	}
	t := bikaRegisterGet(siteU, r.PathValue("task_id"))
	if t == nil {
		v2Fail(w, v2ProviderErr("注册任务不存在或已过期", 404))
		return
	}
	v2OK(w, t)
}

func handleV2ProfileGet(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("source") == string(provider.SourceBika) {
		data, berr := bikaProfileData(r.Context(), effIdentityFor(r, string(provider.SourceBika)))
		if berr != nil {
			v2Fail(w, berr)
			return
		}
		v2OK(w, data)
		return
	}
	if perr := v2ProviderCheck(r.PathValue("source")); perr != nil {
		v2Fail(w, perr)
		return
	}
	raw := store.JmGetProfile(effIdentityOf(r))
	prof := v2UserProfile{
		Source:   "jm",
		Username: v2StrPtr(store.CredActiveUsername(getSiteUser(r))),
		Raw:      raw,
	}
	if raw != nil {
		if n, isStr := raw["username"].(string); isStr {
			prof.Nickname = v2StrPtr(n)
		}
	}
	v2OK(w, prof)
}

func handleV2Checkin(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("source") == string(provider.SourceBika) {
		data, berr := bikaCheckin(r.Context(), effIdentityFor(r, string(provider.SourceBika)))
		if berr != nil {
			v2Fail(w, berr)
			return
		}
		v2OK(w, data)
		return
	}
	if perr := v2ProviderCheck(r.PathValue("source")); perr != nil {
		v2Fail(w, perr)
		return
	}
	identity := effIdentityOf(r)
	uid := store.JmGetUserID(identity)
	if uid == "" {
		v2Fail(w, v2ProviderErr("Missing user_id, please login again", 400))
		return
	}
	ctx := r.Context()
	ck := store.LoadCookies(identity)
	dres, derr := apiClient().Daily(ctx, uid, ck)
	if derr != nil {
		v2Fail(w, derr)
		return
	}
	dailyID := ""
	if dm := rawMap(decodeAny(dres.Data)); dm != nil {
		for _, k := range []string{"daily_id", "id"} {
			if pyTruthy(dm[k]) {
				dailyID = pyStr(dm[k])
				break
			}
		}
		if dailyID == "" {
			for _, k := range []string{"list", "daily_list", "data"} {
				lst := rawList(dm[k])
				if len(lst) == 0 {
					continue
				}
				item := rawMap(lst[0])
				if item == nil {
					continue
				}
				v := item["daily_id"]
				if !pyTruthy(v) {
					v = item["id"]
				}
				if pyTruthy(v) {
					dailyID = pyStr(v)
					break
				}
			}
		}
	}
	if dailyID == "" {
		v2Fail(w, v2ProviderErr("Unable to get daily_id", 400))
		return
	}
	res, cerr := apiClient().DailyCheckIn(ctx, uid, dailyID, ck)
	if cerr != nil {
		v2Fail(w, cerr)
		return
	}
	out := decodeAny(res.Data)
	if _, isMap := out.(map[string]any); !isMap {
		out = map[string]any{"raw": out}
	}
	v2OK(w, out)
}

func handleV2ProfileUpdate(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	var req v2UpdateProfileRequest
	if !v2DecodeBody(w, r, &req) {
		return
	}
	if !v2RequireFields(w, req.Signature) {
		return
	}
	if perr := v2ProviderCheck(source); perr != nil {
		v2Fail(w, perr)
		return
	}
	v2Fail(w, v2ProviderErr("Not supported", 400))
}

func handleV2PasswordUpdate(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	var req v2UpdatePasswordRequest
	if !v2DecodeBody(w, r, &req) {
		return
	}
	if !v2RequireFields(w, req.OldPassword, req.NewPassword) {
		return
	}
	if perr := v2ProviderCheck(source); perr != nil {
		v2Fail(w, perr)
		return
	}
	v2Fail(w, v2ProviderErr("Not supported", 400))
}

func handleV2AvatarUpdate(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	f, _, ferr := r.FormFile("file")
	if ferr != nil {
		httpDetail(w, 422, "Field required")
		return
	}
	f.Close()
	if perr := v2ProviderCheck(source); perr != nil {
		v2Fail(w, perr)
		return
	}
	v2Fail(w, v2ProviderErr("Not supported", 400))
}

// effIdentityForRequest 按 URL 上的 {source} 取隔离键。
// 复用同一份实现的 provider 分发 handler 必须用它，否则会把 jm 源的身份
// 传给别的源（例如 bika），导致对端「登录已过期」。对 jm 源与 effIdentityOf 等价。
func effIdentityForRequest(r *http.Request) string {
	return effIdentityFor(r, r.PathValue("source"))
}

func handleV2Categories(w http.ResponseWriter, r *http.Request) {
	p, perr := provider.For(r.PathValue("source"))
	if perr != nil {
		v2Fail(w, perr)
		return
	}
	out, err := p.Categories(r.Context(), effIdentityForRequest(r))
	if err != nil {
		v2Fail(w, err)
		return
	}
	v2OK(w, out)
}

func handleV2Search(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	qp := r.URL.Query()
	if _, present := qp["q"]; !present {
		httpDetail(w, 422, "Field required")
		return
	}
	q := qp.Get("q")
	page, pok := v2QueryInt(w, r, "page", 1)
	if !pok {
		return
	}
	p, perr := provider.For(source)
	if perr != nil {
		v2Fail(w, perr)
		return
	}
	out, err := p.Search(r.Context(), effIdentityForRequest(r), q, page)
	if err != nil {
		v2Fail(w, err)
		return
	}
	v2OK(w, v2MaskList(r, out))
}

func handleV2Leaderboard(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	page, pok := v2QueryInt(w, r, "page", 1)
	if !pok {
		return
	}
	qp := r.URL.Query()
	category := qp.Get("category")
	if category == "" {
		category = "0"
	}
	sort := qp.Get("sort")
	if sort == "" {
		sort = "tf"
	}
	p, perr := provider.For(source)
	if perr != nil {
		v2Fail(w, perr)
		return
	}
	items, err := p.Leaderboard(r.Context(), effIdentityForRequest(r), category, page, sort, qp.Get("tag"))
	if err != nil {
		v2Fail(w, err)
		return
	}
	v2OK(w, v2MaskList(r, items))
}

func handleV2Random(w http.ResponseWriter, r *http.Request) {
	p, perr := provider.For(r.PathValue("source"))
	if perr != nil {
		v2Fail(w, perr)
		return
	}
	out, err := p.Random(r.Context(), effIdentityForRequest(r))
	if err != nil {
		v2Fail(w, err)
		return
	}
	v2OK(w, v2MaskList(r, out))
}

// v2RandomTarget 是「随机」页一次呈现的漫画数量（jm / 哔咔一致）。
const v2RandomTarget = 10

// v2PickUnique 先用 rnd 打散 items，再按 comic_id 去重并取前 n 条。
// 传 *rand.Rand 而非直接用全局随机源，是为了让「每日精选」这类要求当天稳定的
// 场景能用固定日期种子复现同一结果。
func v2PickUnique(rnd *rand.Rand, items []v2ComicSummary, n int) []v2ComicSummary {
	rnd.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
	seen := make(map[string]struct{}, len(items))
	out := make([]v2ComicSummary, 0, n)
	for _, it := range items {
		id := strings.TrimSpace(it.ComicID)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, it)
		if len(out) >= n {
			break
		}
	}
	return out
}

func handleV2Daily(w http.ResponseWriter, r *http.Request) {
	p, perr := provider.For(r.PathValue("source"))
	if perr != nil {
		v2Fail(w, perr)
		return
	}
	out, err := p.Daily(r.Context(), effIdentityForRequest(r))
	if err != nil {
		v2Fail(w, err)
		return
	}
	v2OK(w, v2MaskList(r, out))
}

func handleV2AlsoViewed(w http.ResponseWriter, r *http.Request) {
	p, perr := provider.For(r.PathValue("source"))
	if perr != nil {
		v2Fail(w, perr)
		return
	}
	out, err := p.AlsoViewed(r.Context(), effIdentityForRequest(r), r.PathValue("comic_id"))
	if err != nil {
		v2Fail(w, err)
		return
	}
	v2OK(w, v2MaskList(r, out))
}

func handleV2ComicDetail(w http.ResponseWriter, r *http.Request) {
	p, perr := provider.For(r.PathValue("source"))
	if perr != nil {
		v2Fail(w, perr)
		return
	}
	out, err := p.ComicDetail(r.Context(), effIdentityForRequest(r), r.PathValue("comic_id"))
	if err != nil {
		v2Fail(w, err)
		return
	}
	v2OK(w, v2MaskDetail(r, out))
}

func handleV2ChapterDetail(w http.ResponseWriter, r *http.Request) {
	p, perr := provider.For(r.PathValue("source"))
	if perr != nil {
		v2Fail(w, perr)
		return
	}
	out, err := p.Chapter(r.Context(), effIdentityForRequest(r), r.PathValue("chapter_id"))
	if err != nil {
		v2Fail(w, err)
		return
	}
	v2OK(w, out)
}

var (
	commentBrRe  = regexp.MustCompile(`(?i)<br\s*/?>`)
	commentTagRe = regexp.MustCompile(`(?is)<[^>]*>`)
	commentGapRe = regexp.MustCompile(`\n{3,}`)

	// commentB64Re matches canonical, padded base64 payloads.
	commentB64Re = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)
)

// sanitizeCommentText strips the HTML shell the upstream forum API wraps
// around every comment body (e.g. <div style='...'>正文</div>) and returns
// plain text ready for direct display.
func sanitizeCommentText(s string) string {
	s = commentBrRe.ReplaceAllString(s, "\n")
	s = commentTagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = commentGapRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// decodeCommentBase64 recovers the plain text of upstream novel replies.
// The platform stores nested replies under "replys" base64-encoded inside an
// HTML shell (e.g. <div style='...'>54mb55qu</div>), so callers must strip the
// markup (sanitizeCommentText) before probing. Comic replies and every
// top-level comment stay plain. The probe is kept deliberately strict - a
// value is only decoded when it is a padded, canonical base64 blob that yields
// valid, printable UTF-8 - so genuine plain text passes through untouched.
func decodeCommentBase64(s string) string {
	t := strings.TrimSpace(s)
	if len(t) < 4 || len(t)%4 != 0 || !commentB64Re.MatchString(t) {
		return s
	}
	raw, err := base64.StdEncoding.DecodeString(t)
	if err != nil || len(raw) == 0 || !utf8.Valid(raw) {
		return s
	}
	for _, r := range string(raw) {
		if r != '\r' && r != '\n' && r != '\t' && !unicode.IsPrint(r) {
			return s
		}
	}
	return string(raw)
}

// sanitizeCommentPayload walks decoded JSON in place, cleaning every string
// stored under a "content" key (comments and nested replies alike). Entries
// inside "replys" are additionally base64-decoded first, since the upstream
// novel forum encodes them.
func sanitizeCommentPayload(v any) any {
	return sanitizeCommentNode(v, false)
}

func sanitizeCommentNode(v any, inReply bool) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			switch {
			case k == "content":
				if s, isStr := val.(string); isStr {
					s = sanitizeCommentText(s)
					if inReply {
						s = decodeCommentBase64(s)
					}
					t[k] = s
					continue
				}
				t[k] = sanitizeCommentNode(val, inReply)
			case k == "replys":
				t[k] = sanitizeReplyList(val)
			default:
				t[k] = sanitizeCommentNode(val, inReply)
			}
		}
		return t
	case []any:
		for i, item := range t {
			t[i] = sanitizeCommentNode(item, inReply)
		}
		return t
	default:
		return v
	}
}

func sanitizeReplyList(v any) any {
	switch t := v.(type) {
	case []any:
		for i, item := range t {
			t[i] = sanitizeCommentNode(item, true)
		}
		return t
	default:
		return sanitizeCommentNode(v, true)
	}
}

func handleV2Comments(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	comicID := r.PathValue("comic_id")
	page, pok := v2QueryInt(w, r, "page", 1)
	if !pok {
		return
	}
	if source == string(provider.SourceBika) {
		data, berr := bikaComments(r.Context(), effIdentityFor(r, string(provider.SourceBika)), comicID, page)
		if berr != nil {
			v2Fail(w, berr)
			return
		}
		v2OK(w, data)
		return
	}
	if perr := v2ProviderCheck(source); perr != nil {
		v2Fail(w, perr)
		return
	}
	cq := url.Values{}
	cq.Set("mode", "manhua")
	if comicID != "" {
		cq.Set("aid", comicID)
	}
	cq.Set("page", strconv.Itoa(page))
	res, err := apiClient().APIGet(r.Context(), "/forum", cq, store.LoadCookies(effIdentityOf(r)))
	if err != nil {
		v2Fail(w, err)
		return
	}
	v2OK(w, sanitizeCommentPayload(decodeAny(res.Data)))
}

// commentTextsForComic 返回某作品评论区**逐条**纯文本（不合并），仅供
// 「评论命中 N 条 → 屏蔽整个作品」判定使用：计的是命中的**评论条数**，不是文本总长。
// 任何失败都返回空——评论拉取失败绝不影响详情本身。
func commentTextsForComic(ctx context.Context, source, identity, comicID string) []string {
	if strings.TrimSpace(comicID) == "" {
		return nil
	}
	if source == string(provider.SourceBika) {
		data, err := bikaComments(ctx, identity, comicID, 1)
		if err != nil {
			return nil
		}
		return collectCommentTexts(data)
	}
	if v2ProviderCheck(source) != nil {
		return nil
	}
	q := url.Values{}
	q.Set("mode", "manhua")
	q.Set("aid", comicID)
	q.Set("page", "1")
	res, err := apiClient().APIGet(ctx, "/forum", q, store.LoadCookies(identity))
	if err != nil {
		return nil
	}
	return collectCommentTexts(sanitizeCommentPayload(decodeAny(res.Data)))
}

// collectCommentTexts 递归收集带非空 content 的评论文本（不做任何删除）。
func collectCommentTexts(node any) []string {
	var out []string
	var walk func(any)
	walk = func(n any) {
		switch v := n.(type) {
		case map[string]any:
			if s, ok := v["content"].(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
			for _, val := range v {
				walk(val)
			}
		case []any:
			for _, e := range v {
				walk(e)
			}
		}
	}
	walk(node)
	return out
}

func handleV2SendComment(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	comicID := r.PathValue("comic_id")
	var req v2SendCommentRequest
	if !v2DecodeBody(w, r, &req) {
		return
	}
	if !v2RequireFields(w, req.Content) {
		return
	}
	if source == string(provider.SourceBika) {
		replyTo := ""
		if req.ReplyTo != nil {
			replyTo = *req.ReplyTo
		}
		data, berr := bikaSendComment(r.Context(), effIdentityFor(r, string(provider.SourceBika)), comicID, *req.Content, replyTo)
		if berr != nil {
			v2Fail(w, berr)
			return
		}
		v2OK(w, data)
		return
	}
	if perr := v2ProviderCheck(source); perr != nil {
		v2Fail(w, perr)
		return
	}
	cid := ""
	if req.ReplyTo != nil {
		cid = *req.ReplyTo
	}
	form := url.Values{}
	form.Set("comment", *req.Content)
	form.Set("aid", comicID)
	if cid != "" {
		form.Set("comment_id", cid)
	}
	res, err := apiClient().APIPost(r.Context(), "/comment", form, store.LoadCookies(effIdentityOf(r)))
	if err != nil {
		v2Fail(w, err)
		return
	}
	v2OK(w, sanitizeCommentPayload(decodeAny(res.Data)))
}

func handleV2LikeComment(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("source") == string(provider.SourceBika) {
		_, berr := bikaLikeComment()
		v2Fail(w, berr)
		return
	}
	if perr := v2ProviderCheck(r.PathValue("source")); perr != nil {
		v2Fail(w, perr)
		return
	}
	res, err := apiClient().LikeComment(r.Context(), r.PathValue("comment_id"), store.LoadCookies(effIdentityOf(r)))
	if err != nil {
		v2Fail(w, err)
		return
	}
	v2OK(w, decodeAny(res.Data))
}

func handleV2ToggleFavorite(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	comicID := r.PathValue("comic_id")
	if source == string(provider.SourceBika) {
		data, berr := bikaToggleFavorite(r.Context(), effIdentityFor(r, string(provider.SourceBika)), comicID)
		if berr != nil {
			v2Fail(w, berr)
			return
		}
		v2OK(w, data)
		return
	}
	if perr := v2ProviderCheck(source); perr != nil {
		v2Fail(w, perr)
		return
	}
	var body struct {
		DesiredState *bool `json:"desired_state"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	identity := effIdentityOf(r)
	current := store.JmIsFavorite(identity, comicID)
	if body.DesiredState != nil && *body.DesiredState == current {
		v2OK(w, map[string]any{"result": map[string]any{"skipped": true}, "is_favorite": current})
		return
	}
	res, err := apiClient().AddFavorite(r.Context(), comicID, store.LoadCookies(identity))
	if err != nil && is401Err(err) && reloginFromSavedConfig(r) {
		res, err = apiClient().AddFavorite(r.Context(), comicID, store.LoadCookies(identity))
	}
	if err != nil {
		v2Fail(w, err)
		return
	}
	raw := decodeAny(res.Data)
	state := !current
	if body.DesiredState != nil {
		state = *body.DesiredState
	}
	if rm, isMap := raw.(map[string]any); isMap {
		opV := rm["type"]
		if !pyTruthy(opV) {
			opV = rm["action"]
		}
		if !pyTruthy(opV) {
			opV = rm["op"]
		}
		switch strings.ToLower(strings.TrimSpace(pyStr(opV))) {
		case "add", "added", "favorite", "fav", "on", "1", "true":
			state = true
		case "del", "delete", "removed", "remove", "unfavorite", "off", "0", "false":
			state = false
		default:
			if bv, isBool := rm["is_favorite"].(bool); isBool {
				state = bv
			}
		}
	}
	store.JmSetFavorite(identity, comicID, state)
	v2OK(w, map[string]any{"result": raw, "is_favorite": state})
}

func handleV2LikeComic(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("source") == string(provider.SourceBika) {
		data, berr := bikaToggleLike(r.Context(), effIdentityFor(r, string(provider.SourceBika)), r.PathValue("comic_id"))
		if berr != nil {
			v2Fail(w, berr)
			return
		}
		v2OK(w, data)
		return
	}
	if perr := v2ProviderCheck(r.PathValue("source")); perr != nil {
		v2Fail(w, perr)
		return
	}
	v2Fail(w, v2ProviderErr("JM comic like not supported in current API", 400))
}

// v2TaskSnapshot reads shared download-task state without touching dl.go.
// 仅返回归属 owner（站点用户名）的任务，避免任务 ID 泄漏后被他人取走打包文件。
func v2TaskSnapshot(id, owner string) (pub map[string]any, status, zipPath string, found bool) {
	return taskManager.ownedSnapshot(id, owner)
}

func handleV2CreateDownloadTask(w http.ResponseWriter, r *http.Request) {
	// 下载会长时间占用 CPU/带宽，限制为登录用户可用。
	owner, okc := requireSiteUser(w, r)
	if !okc {
		return
	}
	source := r.PathValue("source")
	var req v2DownloadTaskRequest
	if !v2DecodeBody(w, r, &req) {
		return
	}
	if !v2RequireFields(w, req.ComicID) {
		return
	}
	// 无损 PNG 为爱发电赞助者专享（绑定一次永久有效，见 /api/afdian/binding）。
	if req.Lossless && !store.HasAfdianBinding(owner) {
		writeJSON(w, 200, errSt(StatusUserError, "无损下载为爱发电赞助者专享，请在「设置」中绑定赞助订单号"))
		return
	}
	if !isBikaSource(source) {
		if perr := v2ProviderCheck(source); perr != nil {
			v2Fail(w, perr)
			return
		}
	}
	var cached map[string]any
	fetchDetail := func() (map[string]any, error) {
		if cached != nil {
			return cached, nil
		}
		// 章节展开/取标题必须按源进行：bika 与 jm 的详情接口完全不同。
		if isBikaSource(source) {
			d, berr := bikaComicDetailData(r.Context(), effIdentityFor(r, string(provider.SourceBika)), *req.ComicID)
			if berr != nil {
				return nil, berr
			}
			eps := make([]map[string]any, 0, len(d.Chapters))
			for _, c := range d.Chapters {
				eps = append(eps, map[string]any{"id": c.ID, "title": c.Title})
			}
			cached = map[string]any{"title": d.Title, "episode_list": eps}
			return cached, nil
		}
		aq := url.Values{}
		aq.Set("comicName", "")
		aq.Set("id", *req.ComicID)
		res, err := apiClient().APIGet(r.Context(), "/album", aq, store.LoadCookies(effIdentityOf(r)))
		if err != nil {
			return nil, err
		}
		cached = adaptAlbumDetail(decodeRawMap(res.Data))
		return cached, nil
	}
	chapters := make([]downloadChapter, 0, len(req.Chapters))
	for _, cm := range req.Chapters {
		chapters = append(chapters, downloadChapter{ID: cm["id"], Title: cm["title"]})
	}
	if req.IncludeAll || len(chapters) == 0 {
		d, ferr := fetchDetail()
		if ferr != nil {
			v2Fail(w, ferr)
			return
		}
		chapters = []downloadChapter{}
		if eps, ok2 := d["episode_list"].([]map[string]any); ok2 {
			for _, ep := range eps {
				cid := pyStr(ep["id"])
				if cid == "" {
					continue
				}
				ct := pyStr(ep["title"])
				if ct == "" {
					ct = cid
				}
				chapters = append(chapters, downloadChapter{ID: cid, Title: ct})
			}
		}
	}
	title := ""
	if req.ComicTitle != nil {
		title = *req.ComicTitle
	}
	if title == "" {
		d, terr := fetchDetail()
		if terr != nil || d == nil {
			title = *req.ComicID
		} else {
			title = pyStr(d["title"])
		}
	}
	if source == "jm" || isBikaSource(source) {
		var task *downloadTask
		var err error
		if isBikaSource(source) {
			task, err = taskManager.createTaskSource(*req.ComicID, title, chapters, effIdentityFor(r, string(provider.SourceBika)), source, owner, req.Lossless)
		} else {
			task, err = taskManager.createTask(*req.ComicID, title, chapters, effIdentityOf(r), owner, req.Lossless)
		}
		if err != nil {
			if errors.Is(err, errDlQueueFull) {
				v2Fail(w, v2ProviderErr(fmt.Sprintf("排队中的下载任务已达上限（%d 个），请等待现有任务完成后再试", taskManager.maxQueued), 429))
				return
			}
			v2Fail(w, v2ProviderErr("创建下载任务失败", 500))
			return
		}
		pub, _, _, _ := taskManager.ownedSnapshot(task.TaskID, owner)
		dlURL := ""
		if task.Status == "completed" && task.ZipPath != "" {
			dlURL = fmt.Sprintf("/api/v2/%s/download/tasks/%s/download", source, task.TaskID)
		}
		pub["download_url"] = dlURL
		v2OK(w, pub)
		return
	}
	v2Fail(w, v2ProviderErr("Unknown source", 400))
}

func handleV2GetDownloadTask(w http.ResponseWriter, r *http.Request) {
	owner, okc := requireSiteUser(w, r)
	if !okc {
		return
	}
	source := r.PathValue("source")
	taskID := r.PathValue("task_id")
	if source == "jm" || isBikaSource(source) {
		pub, status, zipPath, found := v2TaskSnapshot(taskID, owner)
		if !found {
			v2Fail(w, v2ProviderErr("Task not found", 404))
			return
		}
		if status == "completed" && zipPath != "" {
			pub["download_url"] = fmt.Sprintf("/api/v2/%s/download/tasks/%s/download", source, taskID)
		}
		v2OK(w, pub)
		return
	}
	v2Fail(w, v2ProviderErr("Unknown source", 400))
}

func handleV2CancelDownloadTask(w http.ResponseWriter, r *http.Request) {
	owner, okc := requireSiteUser(w, r)
	if !okc {
		return
	}
	source := r.PathValue("source")
	taskID := r.PathValue("task_id")
	// 归属校验：非本人任务一律视为不存在。
	if _, _, _, found := v2TaskSnapshot(taskID, owner); !found {
		v2Fail(w, v2ProviderErr("Task not found", 404))
		return
	}
	if source == "jm" || isBikaSource(source) {
		if taskManager.cancelQueued(taskID) {
			v2OK(w, map[string]any{"status": "cancelled"})
			return
		}
		v2Fail(w, v2ProviderErr("Task not found or cannot be cancelled", 400))
		return
	}
	v2Fail(w, v2ProviderErr("Unknown source", 400))
}

func handleV2DownloadTaskZip(w http.ResponseWriter, r *http.Request) {
	owner, okc := requireSiteUser(w, r)
	if !okc {
		return
	}
	if sc := r.PathValue("source"); sc != "jm" && !isBikaSource(sc) {
		httpDetail(w, 400, "Unknown source")
		return
	}
	_, status, zipPath, found := v2TaskSnapshot(r.PathValue("task_id"), owner)
	if !found || status != "completed" || zipPath == "" {
		httpDetail(w, 404, "Zip not available")
		return
	}
	f, oerr := os.Open(zipPath)
	if oerr != nil {
		httpDetail(w, 404, "File not found")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(zipPath)))
	if fi, serr := f.Stat(); serr == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func handleV2CacheCleanup(w http.ResponseWriter, r *http.Request) {
	keepDays, pok := v2QueryInt(w, r, "keep_days", 7)
	if !pok {
		return
	}
	dirs, work := dlCleanupCache(keepDays)
	writeJSON(w, 200, ok(map[string]any{"removed_dirs": dirs, "removed_work": work}, ""))
}

// handleV2CacheStats 暴露上游响应缓存与章节图解码缓存的运行数据，
// 用于确认优化是否真的生效（命中率、被合并的请求数、占用等）。
func handleV2CacheStats(w http.ResponseWriter, r *http.Request) {
	imgEntries, imgBytes := imgDecoded.stats()
	upstream := jm.SharedAPICacheStats()
	writeJSON(w, 200, ok(map[string]any{
		"upstream_api": map[string]any{
			"enabled":            upstream.Enabled,
			"entries":            upstream.Entries,
			"bytes":              upstream.Bytes,
			"max_bytes":          upstream.MaxBytes,
			"hits":               upstream.Hits,
			"misses":             upstream.Misses,
			"upstream_calls":     upstream.UpstreamCalls,
			"expired":            upstream.Expired,
			"coalesced":          upstream.Coalesced,
			"evicted":            upstream.Evicted,
			"rejected_too_large": upstream.Rejected,
			"errors_cached":      upstream.ErrorsCached,
			"hit_rate":           upstream.HitRate,
		},
		"chapter_images_decoded": map[string]any{
			"entries": imgEntries,
			"bytes":   imgBytes,
		},
	}, ""))
}

type v2NovelSummary struct {
	Source   string         `json:"source"`
	NovelID  string         `json:"novel_id"`
	Title    string         `json:"title"`
	Author   *string        `json:"author"`
	CoverURL *string        `json:"cover_url"`
	Category *string        `json:"category"`
	Raw      map[string]any `json:"raw"`
}

type v2NovelChapterSummary struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Order int    `json:"order"`
}

type v2NovelDetail struct {
	Source      string                  `json:"source"`
	NovelID     string                  `json:"novel_id"`
	Title       string                  `json:"title"`
	Author      *string                 `json:"author"`
	CoverURL    *string                 `json:"cover_url"`
	Description *string                 `json:"description"`
	Tags        []string                `json:"tags"`
	Category    *string                 `json:"category"`
	IsFavorite  *bool                   `json:"is_favorite"`
	Chapters    []v2NovelChapterSummary `json:"chapters"`
	Raw         map[string]any          `json:"raw"`
}

type v2NovelChapterDetail struct {
	Source    string         `json:"source"`
	ChapterID string         `json:"chapter_id"`
	Title     *string        `json:"title"`
	Content   string         `json:"content"`
	Raw       map[string]any `json:"raw"`
}

func v2NovelSummaryFromAdapt(it map[string]any) v2NovelSummary {
	return v2NovelSummary{
		Source:   "jm",
		NovelID:  pyStr(it["novel_id"]),
		Title:    pyStr(it["title"]),
		Author:   v2StrPtr(authorText(it["author"])),
		CoverURL: v2StrPtr(pyStr(it["image"])),
		Category: v2StrPtr(pyStr(it["category"])),
		Raw:      it,
	}
}

func handleV2NovelList(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	page, pok := v2QueryInt(w, r, "page", 1)
	if !pok {
		return
	}
	if perr := v2ProviderCheck(source); perr != nil {
		v2Fail(w, perr)
		return
	}
	order := r.URL.Query().Get("o")
	if order == "" {
		order = "mr"
	}
	res, err := apiClient().NovelList(r.Context(), page, order, store.LoadCookies(effIdentityOf(r)))
	if err != nil {
		v2Fail(w, err)
		return
	}
	items := adaptNovelList(decodeAny(res.Data))
	out := make([]v2NovelSummary, 0, len(items))
	for _, it := range items {
		if pyStr(it["novel_id"]) == "" {
			continue
		}
		out = append(out, v2NovelSummaryFromAdapt(it))
	}
	raw := rawMap(decodeAny(res.Data))
	total := 0
	if raw != nil && pyTruthy(raw["total"]) {
		total = toInt(raw["total"])
	}
	v2OK(w, map[string]any{"list": out, "total": total})
}

func handleV2NovelSearch(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	qp := r.URL.Query()
	q := qp.Get("q")
	if q == "" {
		httpDetail(w, 422, "Field required")
		return
	}
	page, pok := v2QueryInt(w, r, "page", 1)
	if !pok {
		return
	}
	if perr := v2ProviderCheck(source); perr != nil {
		v2Fail(w, perr)
		return
	}
	order := qp.Get("o")
	if order == "" {
		order = "mr"
	}
	res, err := apiClient().NovelSearch(r.Context(), q, page, order, store.LoadCookies(effIdentityOf(r)))
	if err != nil {
		v2Fail(w, err)
		return
	}
	items := adaptNovelList(decodeAny(res.Data))
	out := make([]v2NovelSummary, 0, len(items))
	for _, it := range items {
		if pyStr(it["novel_id"]) == "" {
			continue
		}
		out = append(out, v2NovelSummaryFromAdapt(it))
	}
	raw := rawMap(decodeAny(res.Data))
	total := 0
	redirectAid := ""
	if raw != nil {
		if pyTruthy(raw["total"]) {
			total = toInt(raw["total"])
		}
		redirectAid = strings.TrimSpace(pyStr(raw["redirect_aid"]))
	}
	v2OK(w, map[string]any{"list": out, "total": total, "redirect_aid": redirectAid})
}

func handleV2NovelDetail(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	nid := r.PathValue("novel_id")
	if perr := v2ProviderCheck(source); perr != nil {
		v2Fail(w, perr)
		return
	}
	res, err := apiClient().NovelDetail(r.Context(), nid, store.LoadCookies(effIdentityOf(r)))
	if err != nil {
		v2Fail(w, err)
		return
	}
	d := adaptNovelDetail(decodeRawMap(res.Data))
	chapters := []v2NovelChapterSummary{}
	if eps, ok2 := d["chapter_list"].([]map[string]any); ok2 {
		for idx, ep := range eps {
			cid := pyStr(ep["id"])
			if cid == "" {
				continue
			}
			chapters = append(chapters, v2NovelChapterSummary{ID: cid, Title: pyStr(ep["title"]), Order: idx})
		}
	}
	nidOut := pyStr(d["novel_id"])
	if nidOut == "" {
		nidOut = nid
	}
	tags, _ := d["tags"].([]string)
	if tags == nil {
		tags = []string{}
	}
	var isFav *bool
	if pv, okp := d["is_favorite"].(*bool); okp {
		isFav = pv
	}
	v2OK(w, v2NovelDetail{
		Source:      "jm",
		NovelID:     nidOut,
		Title:       pyStr(d["title"]),
		Author:      v2StrPtr(authorText(d["author"])),
		CoverURL:    v2StrPtr(pyStr(d["image"])),
		Description: v2StrPtr(pyStr(d["description"])),
		Tags:        tags,
		IsFavorite:  isFav,
		Chapters:    chapters,
		Raw:         d,
	})
}

func handleV2NovelChapter(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	ncid := r.PathValue("chapter_id")
	if perr := v2ProviderCheck(source); perr != nil {
		v2Fail(w, perr)
		return
	}
	lang := r.URL.Query().Get("lang")
	if lang == "" {
		lang = "tw"
	}
	res, err := apiClient().NovelChapter(r.Context(), ncid, lang, store.LoadCookies(effIdentityOf(r)))
	if err != nil {
		v2Fail(w, err)
		return
	}
	d := adaptNovelChapter(decodeRawMap(res.Data))
	v2OK(w, v2NovelChapterDetail{
		Source:    "jm",
		ChapterID: pyStr(d["chapter_id"]),
		Title:     v2StrPtr(pyStr(d["title"])),
		Content:   pyStr(d["content"]),
		Raw:       d,
	})
}

func handleV2NovelFavorites(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	page, pok := v2QueryInt(w, r, "page", 1)
	if !pok {
		return
	}
	if perr := v2ProviderCheck(source); perr != nil {
		v2Fail(w, perr)
		return
	}
	// JM 官方小说收藏列表默认 folder_id 为空串（表示"全部"），此处保持一致。
	fid := r.URL.Query().Get("folder_id")
	order := r.URL.Query().Get("o")
	if order == "" {
		order = "mr"
	}
	ck := store.LoadCookies(effIdentityOf(r))
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	res, err := apiClient().NovelFavorites(ctx, page, fid, order, ck)
	if err != nil && is401Err(err) && reloginFromSavedConfig(r) {
		res, err = apiClient().NovelFavorites(ctx, page, fid, order, store.LoadCookies(effIdentityOf(r)))
	}
	if err != nil {
		v2Fail(w, err)
		return
	}
	data := adaptNovelFavorites(decodeAny(res.Data))
	items, _ := data["content"].([]map[string]any)
	out := make([]v2NovelSummary, 0, len(items))
	for _, it := range items {
		if pyStr(it["novel_id"]) == "" {
			continue
		}
		out = append(out, v2NovelSummaryFromAdapt(it))
	}
	folders, _ := data["folders"].([]map[string]string)
	v2OK(w, map[string]any{
		"list":    out,
		"total":   data["total"],
		"pages":   data["pages"],
		"folders": folders,
	})
}

func handleV2NovelLike(w http.ResponseWriter, r *http.Request) {
	if perr := v2ProviderCheck(r.PathValue("source")); perr != nil {
		v2Fail(w, perr)
		return
	}
	nid := r.PathValue("novel_id")
	res, err := apiClient().NovelLike(r.Context(), nid, store.LoadCookies(effIdentityOf(r)))
	if err != nil {
		v2Fail(w, err)
		return
	}
	v2OK(w, decodeAny(res.Data))
}

// v2FavStateFromRaw derives a favourite state from an add/remove response.
func v2FavStateFromRaw(raw any) (bool, bool) {
	rm, isMap := raw.(map[string]any)
	if !isMap {
		return false, false
	}
	opV := rm["type"]
	if !pyTruthy(opV) {
		opV = rm["action"]
	}
	if !pyTruthy(opV) {
		opV = rm["op"]
	}
	switch strings.ToLower(strings.TrimSpace(pyStr(opV))) {
	case "add", "added", "favorite", "fav", "on", "1", "true":
		return true, true
	case "del", "delete", "removed", "remove", "unfavorite", "off", "0", "false":
		return false, true
	}
	if bv, isBool := rm["is_favorite"].(bool); isBool {
		return bv, true
	}
	return false, false
}

// handleV2NovelToggleFavorite relays a novel favourite add/remove straight to
// JM. Novel favourite state is intentionally NOT cached locally.
func handleV2NovelToggleFavorite(w http.ResponseWriter, r *http.Request) {
	if perr := v2ProviderCheck(r.PathValue("source")); perr != nil {
		v2Fail(w, perr)
		return
	}
	nid := r.PathValue("novel_id")
	var body struct {
		DesiredState *bool `json:"desired_state"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	identity := effIdentityOf(r)
	res, err := apiClient().NovelFavoritesToggle(r.Context(), nid, store.LoadCookies(identity))
	if err != nil && is401Err(err) && reloginFromSavedConfig(r) {
		res, err = apiClient().NovelFavoritesToggle(r.Context(), nid, store.LoadCookies(identity))
	}
	if err != nil {
		v2Fail(w, err)
		return
	}
	raw := decodeAny(res.Data)
	state := false
	if body.DesiredState != nil {
		state = *body.DesiredState
	}
	if sv, found := v2FavStateFromRaw(raw); found {
		state = sv
	}
	v2OK(w, map[string]any{"result": raw, "is_favorite": state})
}

// handleV2NovelFavoriteFolder relays a novel favourite-folder operation
// (add/rename/move/del) to JM with no local caching.
func handleV2NovelFavoriteFolder(w http.ResponseWriter, r *http.Request) {
	if perr := v2ProviderCheck(r.PathValue("source")); perr != nil {
		v2Fail(w, perr)
		return
	}
	nid := r.PathValue("novel_id")
	var body struct {
		Type       string `json:"type"`
		FolderID   string `json:"folder_id"`
		FolderName string `json:"folder_name"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	identity := effIdentityOf(r)
	op := func() (*jm.CallResult, error) {
		return apiClient().NovelFavoritesFolderOp(r.Context(), body.Type, body.FolderID, body.FolderName, nid, store.LoadCookies(identity))
	}
	res, err := op()
	if err != nil && is401Err(err) && reloginFromSavedConfig(r) {
		res, err = op()
	}
	if err != nil {
		v2Fail(w, err)
		return
	}
	v2OK(w, map[string]any{"result": decodeAny(res.Data)})
}

func handleV2NovelComments(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	nid := r.PathValue("novel_id")
	if perr := v2ProviderCheck(source); perr != nil {
		v2Fail(w, perr)
		return
	}
	page, pok := v2QueryInt(w, r, "page", 1)
	if !pok {
		return
	}
	res, err := apiClient().NovelComments(r.Context(), nid, page, store.LoadCookies(effIdentityOf(r)))
	if err != nil {
		v2Fail(w, err)
		return
	}
	v2OK(w, sanitizeCommentPayload(decodeAny(res.Data)))
}

func handleV2NovelSendComment(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	nid := r.PathValue("novel_id")
	if perr := v2ProviderCheck(source); perr != nil {
		v2Fail(w, perr)
		return
	}
	var body struct {
		Content   string `json:"content"`
		CommentID string `json:"comment_id"`
	}
	if !v2DecodeBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		httpDetail(w, 422, "Field required")
		return
	}
	res, err := apiClient().NovelSendComment(r.Context(), nid, body.Content, body.CommentID, store.LoadCookies(effIdentityOf(r)))
	if err != nil {
		v2Fail(w, err)
		return
	}
	raw := decodeAny(res.Data)
	if rm, isMap := raw.(map[string]any); isMap && strings.ToLower(pyStr(rm["status"])) == "fail" {
		msg := pyStr(rm["msg"])
		if msg == "" {
			msg = "Failed to post comment"
		}
		v2Fail(w, v2ProviderErr(msg, 400))
		return
	}
	v2OK(w, raw)
}

// ---- 小说导出：选话 / 繁简 / txt·epub，后台任务 + 进度 + 完成后下载 ----

type v2NovelExportRequest struct {
	NovelID    *string             `json:"novel_id"`
	NovelTitle *string             `json:"novel_title"`
	Author     *string             `json:"author"`
	Chapters   []map[string]string `json:"chapters"`
	IncludeAll bool                `json:"include_all"`
	Lang       string              `json:"lang"`
	Format     string              `json:"format"`
}

func handleV2NovelExport(w http.ResponseWriter, r *http.Request) {
	// 下载会长时间占用 CPU/带宽，限制为登录用户可用。
	owner, okc := requireSiteUser(w, r)
	if !okc {
		return
	}
	source := r.PathValue("source")
	if perr := v2ProviderCheck(source); perr != nil {
		v2Fail(w, perr)
		return
	}
	var req v2NovelExportRequest
	if !v2DecodeBody(w, r, &req) {
		return
	}
	if !v2RequireFields(w, req.NovelID) {
		return
	}
	lang := strings.ToLower(strings.TrimSpace(req.Lang))
	if lang != "cn" {
		lang = "tw"
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format != "epub" {
		format = "txt"
	}

	var cached map[string]any
	fetchDetail := func() (map[string]any, error) {
		if cached != nil {
			return cached, nil
		}
		res, err := apiClient().NovelDetail(r.Context(), *req.NovelID, store.LoadCookies(effIdentityOf(r)))
		if err != nil {
			return nil, err
		}
		cached = adaptNovelDetail(decodeRawMap(res.Data))
		return cached, nil
	}

	chapters := make([]novelExportChapter, 0, len(req.Chapters))
	for _, cm := range req.Chapters {
		cid := strings.TrimSpace(cm["id"])
		if cid == "" {
			continue
		}
		chapters = append(chapters, novelExportChapter{ID: cid, Title: cm["title"], Order: len(chapters)})
	}
	if req.IncludeAll || len(chapters) == 0 {
		d, ferr := fetchDetail()
		if ferr != nil {
			v2Fail(w, ferr)
			return
		}
		chapters = []novelExportChapter{}
		if eps, ok2 := d["chapter_list"].([]map[string]any); ok2 {
			for _, ep := range eps {
				cid := pyStr(ep["id"])
				if cid == "" {
					continue
				}
				chapters = append(chapters, novelExportChapter{ID: cid, Title: pyStr(ep["title"]), Order: len(chapters)})
			}
		}
	}
	if len(chapters) == 0 {
		v2Fail(w, v2ProviderErr("未选择章节", 400))
		return
	}

	title, author := "", ""
	if req.NovelTitle != nil {
		title = strings.TrimSpace(*req.NovelTitle)
	}
	if req.Author != nil {
		author = strings.TrimSpace(*req.Author)
	}
	if title == "" || author == "" {
		if d, derr := fetchDetail(); derr == nil && d != nil {
			if title == "" {
				title = pyStr(d["title"])
			}
			if author == "" {
				author = authorText(d["author"])
			}
		}
	}
	if title == "" {
		title = *req.NovelID
	}

	task, err := novelExporter.createTask(&novelExportTask{
		NovelID:    *req.NovelID,
		NovelTitle: title,
		Author:     author,
		Chapters:   chapters,
		Lang:       lang,
		Format:     format,
		identity:   effIdentityOf(r),
		owner:      owner,
	})
	if err != nil {
		if errors.Is(err, errDlQueueFull) {
			v2Fail(w, v2ProviderErr(fmt.Sprintf("排队中的导出任务已达上限（%d 个），请等待现有任务完成后再试", novelExporter.maxQueued), 429))
			return
		}
		v2Fail(w, v2ProviderErr("创建导出任务失败", 500))
		return
	}
	pub, _, _, _ := novelExporter.snapshot(task.TaskID, owner)
	v2OK(w, pub)
}

func handleV2GetNovelExport(w http.ResponseWriter, r *http.Request) {
	owner, okc := requireSiteUser(w, r)
	if !okc {
		return
	}
	if perr := v2ProviderCheck(r.PathValue("source")); perr != nil {
		v2Fail(w, perr)
		return
	}
	pub, _, _, found := novelExporter.snapshot(r.PathValue("task_id"), owner)
	if !found {
		v2Fail(w, v2ProviderErr("Task not found", 404))
		return
	}
	v2OK(w, pub)
}

func handleV2CancelNovelExport(w http.ResponseWriter, r *http.Request) {
	owner, okc := requireSiteUser(w, r)
	if !okc {
		return
	}
	if perr := v2ProviderCheck(r.PathValue("source")); perr != nil {
		v2Fail(w, perr)
		return
	}
	// 归属校验：非本人任务一律视为不存在。
	if _, _, _, found := novelExporter.snapshot(r.PathValue("task_id"), owner); !found {
		v2Fail(w, v2ProviderErr("Task not found or cannot be cancelled", 400))
		return
	}
	if novelExporter.cancelQueued(r.PathValue("task_id")) {
		v2OK(w, map[string]any{"status": "cancelled"})
		return
	}
	v2Fail(w, v2ProviderErr("Task not found or cannot be cancelled", 400))
}

func handleV2NovelExportDownload(w http.ResponseWriter, r *http.Request) {
	owner, okc := requireSiteUser(w, r)
	if !okc {
		return
	}
	if r.PathValue("source") != "jm" {
		httpDetail(w, 400, "Unknown source")
		return
	}
	pub, status, filePath, found := novelExporter.snapshot(r.PathValue("task_id"), owner)
	if !found || status != "completed" || filePath == "" {
		httpDetail(w, 404, "File not available")
		return
	}
	f, oerr := os.Open(filePath)
	if oerr != nil {
		httpDetail(w, 404, "File not found")
		return
	}
	defer f.Close()
	ctype := "text/plain; charset=utf-8"
	if fmtName, _ := pub["format"].(string); fmtName == "epub" {
		ctype = "application/epub+zip"
	}
	name := filepath.Base(filePath)
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", name, url.PathEscape(name)))
	if fi, serr := f.Stat(); serr == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}
