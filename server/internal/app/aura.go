package app

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"jmaura/internal/store"
)

// readerSettingsPayload 是阅读器设置的读写载荷。全部用指针接收，
// 这样 PUT 时能区分「没传这个字段」（保持原值）和「显式传了 0」。
type readerSettingsPayload struct {
	Width     *int `json:"width"`
	Gap       *int `json:"gap"`
	Initial   *int `json:"initial"`
	Batch     *int `json:"batch"`
	KeepPages *int `json:"keep_pages"`
}

// clampInt 把越界值收进区间；区间与前端滑块保持一致。
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// 服务端只做区间钳制、不改默认值：默认值属于前端表现层，以后调默认值不必动后端。
// 非法/缺失的字段直接丢弃，让前端继续用它自己的默认值。
func sanitizeReaderSettings(in readerSettingsPayload) map[string]any {
	out := map[string]any{}
	put := func(key string, v *int, lo, hi int) {
		if v == nil {
			return
		}
		out[key] = clampInt(*v, lo, hi)
	}
	put("width", in.Width, 20, 100)
	put("gap", in.Gap, 0, 50)
	put("initial", in.Initial, 1, 12)
	put("batch", in.Batch, 1, 6)
	// 保留页数直接决定阅读器同时持有的画布数量（内存），必须卡死在 1..4。
	put("keep_pages", in.KeepPages, 1, 4)
	return out
}

func handleAuraReaderSettingsGet(w http.ResponseWriter, r *http.Request) {
	u, okc := auraUserOf(w, r)
	if !okc {
		return
	}
	writeJSON(w, 200, ok(store.AuraGetReaderSettings(u), ""))
}

func handleAuraReaderSettingsPut(w http.ResponseWriter, r *http.Request) {
	u, okc := auraUserOf(w, r)
	if !okc {
		return
	}
	var req readerSettingsPayload
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, 200, errSt(StatusUserError, "Invalid request"))
		return
	}
	settings := sanitizeReaderSettings(req)
	if len(settings) == 0 {
		// 没有任何可识别字段：不报错，回当前值，避免前端一次空保存把记录清掉。
		writeJSON(w, 200, ok(store.AuraGetReaderSettings(u), ""))
		return
	}
	// 与已存记录合并后再整体写回：局部 PUT 不会清掉其它字段。
	merged := store.AuraGetReaderSettings(u)
	if merged == nil {
		merged = map[string]any{}
	}
	for k, v := range settings {
		merged[k] = v
	}
	store.AuraSaveReaderSettings(u, merged)
	writeJSON(w, 200, ok(merged, ""))
}

type auraHistoryPushRequest struct {
	AlbumID    string   `json:"album_id"`
	AlbumTitle *string  `json:"album_title"`
	PhotoID    *string  `json:"photo_id"`
	Title      *string  `json:"title"`
	PageIndex  *int64   `json:"page_index"`
	Timestamp  *int64   `json:"timestamp"`
	Type       *string  `json:"type"`
	ScrollPct  *float64 `json:"scroll_pct"`
	Source     *string  `json:"source"`
	Lang       *string  `json:"lang"`
}

func auraUserOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	u := getSiteUser(r)
	if u == "" {
		writeJSON(w, 401, errSt(StatusNotLogin, "Not authenticated"))
		return "", false
	}
	return u, true
}

func handleAuraHistoryGet(w http.ResponseWriter, r *http.Request) {
	u, okc := auraUserOf(w, r)
	if !okc {
		return
	}
	limit := 50
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, perr := strconv.Atoi(v); perr == nil {
			limit = n
		} else {
			httpDetail(w, 422, "Invalid limit")
			return
		}
	}
	writeJSON(w, 200, ok(store.AuraListHistory(u, limit), ""))
}

func handleAuraHistoryPost(w http.ResponseWriter, r *http.Request) {
	u, okc := auraUserOf(w, r)
	if !okc {
		return
	}
	var req auraHistoryPushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 200, errSt(StatusUserError, "Invalid request"))
		return
	}
	pageIndex := int64(0)
	hasPage := req.PageIndex != nil
	if hasPage {
		pageIndex = *req.PageIndex
	}
	ts := int64(0)
	if req.Timestamp != nil {
		ts = *req.Timestamp
	}
	itemType := ""
	if req.Type != nil {
		itemType = strings.TrimSpace(*req.Type)
	}
	scrollPct := 0.0
	hasScroll := req.ScrollPct != nil
	if hasScroll {
		scrollPct = *req.ScrollPct
	}
	albumTitle := ""
	if req.AlbumTitle != nil {
		albumTitle = *req.AlbumTitle
	}
	photoID := ""
	if req.PhotoID != nil {
		photoID = *req.PhotoID
	}
	title := ""
	if req.Title != nil {
		title = *req.Title
	}
	source := ""
	if req.Source != nil {
		source = strings.TrimSpace(*req.Source)
	}
	lang := ""
	if req.Lang != nil {
		lang = strings.TrimSpace(*req.Lang)
	}
	aerr := store.AuraPushHistory(u, req.AlbumID, albumTitle, photoID, title, pageIndex, hasPage, ts, itemType, scrollPct, hasScroll, source, lang)
	if aerr != nil {
		msg := aerr.Error()
		if msg == "" {
			msg = "Invalid request"
		}
		writeJSON(w, 200, errSt(StatusUserError, msg))
		return
	}
	writeJSON(w, 200, ok(map[string]any{"status": "success"}, ""))
}
