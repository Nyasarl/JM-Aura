package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"jmaura/internal/jm"
	"jmaura/internal/store"
)

var (
	reScrambleID     = regexp.MustCompile(`(?i)scramble[_\s-]?id\s*[:=]\s*(\d+)`)
	reDataOrigDomEq  = regexp.MustCompile(`(?i)data-original-domain\s*=\s*"([^"]+)"`)
	reDataOrigDomCol = regexp.MustCompile(`(?i)data_original_domain\s*[:=]\s*"([^"]+)"`)
	reDataOriginal   = regexp.MustCompile(`(?i)data-original\s*=\s*"(https?://[^"]+)"`)
)

type folderTooLarge struct{ total int }

func (e *folderTooLarge) Error() string { return strconv.Itoa(e.total) }

func decodeAny(b []byte) any {
	if len(b) == 0 {
		return map[string]any{}
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return string(b)
	}
	return v
}

func lastSeg(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func apiErrMsg(err error) string {
	var ae *jm.APIError
	if errors.As(err, &ae) {
		return ae.Msg
	}
	msg := strings.TrimPrefix(err.Error(), "API Error:")
	return strings.TrimSpace(msg)
}

func asIntID(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func parseChapterViewTemplate(html string) map[string]any {
	if html == "" {
		return map[string]any{"scramble_id": "220980", "data_original_domain": nil}
	}
	scrambleID := "220980"
	var domain any
	if m := reScrambleID.FindStringSubmatch(html); m != nil {
		scrambleID = m[1]
	}
	if m := reDataOrigDomEq.FindStringSubmatch(html); m != nil {
		domain = m[1]
	}
	if domain == nil {
		if m := reDataOrigDomCol.FindStringSubmatch(html); m != nil {
			domain = m[1]
		}
	}
	if domain == nil {
		if m := reDataOriginal.FindStringSubmatch(html); m != nil {
			if u, uerr := url.Parse(m[1]); uerr == nil {
				domain = u.Host
			}
		}
	}
	return map[string]any{"scramble_id": scrambleID, "data_original_domain": domain}
}

func fetchFolders(cookies map[string]string) ([]map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	q := url.Values{}
	q.Set("page", "1")
	q.Set("folder_id", "0")
	q.Set("o", "mr")
	res, err := apiClient().APIGet(ctx, "/favorite", q, cookies)
	if err != nil {
		return nil, err
	}
	d0 := adaptFavorites(decodeAny(res.Data))
	fl, _ := d0["folders"].([]map[string]string)
	return fl, nil
}

func folderOp(cookies map[string]string, form url.Values) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	res, err := apiClient().APIPost(ctx, "/favorite_folder", form, cookies)
	if err != nil {
		return nil, err
	}
	return decodeAny(res.Data), nil
}

func isFailStatus(raw any) bool {
	m, isMap := raw.(map[string]any)
	if !isMap {
		return false
	}
	return strings.ToLower(pyStr(m["status"])) == "fail"
}

func findFolder(folders []map[string]string, fid string) map[string]string {
	for _, f := range folders {
		if f["id"] == fid {
			return f
		}
	}
	return nil
}

func verifyFolders(tries int, cookies map[string]string, done func([]map[string]string) bool) (folders []map[string]string, lastMsg string, matched bool, err error) {
	folders = []map[string]string{}
	errs := 0
	for i := 0; i < tries; i++ {
		fl, ferr := fetchFolders(cookies)
		if ferr != nil {
			if is401Err(ferr) {
				return folders, lastMsg, false, ferr
			}
			errs++
			lastMsg = ferr.Error()
			if errs >= 2 {
				break
			}
		} else {
			folders = fl
			if done(fl) {
				return folders, "", true, nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return folders, lastMsg, false, nil
}

func favoriteQuery(page int, fid string) url.Values {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("folder_id", fid)
	q.Set("o", "mr")
	return q
}

func handlePromote(w http.ResponseWriter, r *http.Request) {
	page := normalizePageParam(r.URL.Query().Get("page"))
	var raw []byte
	if body, has := promoteGet(page); has {
		raw = body
	} else {
		ck := store.LoadCookies(effIdentityOf(r))
		res, err := apiClient().Promote(r.Context(), page, ck)
		if err != nil {
			httpDetail(w, 500, err.Error())
			return
		}
		promotePut(page, res.Data)
		raw = res.Data
	}
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		writeJSON(w, 200, json.RawMessage(raw))
		return
	}
	writeJSON(w, 200, v2MaskLegacy(r, data, true))
}

func handleLatest(w http.ResponseWriter, r *http.Request) {
	page := normalizePageParam(r.URL.Query().Get("page"))
	ck := store.LoadCookies(effIdentityOf(r))
	res, err := apiClient().Latest(r.Context(), page, ck)
	if err != nil {
		httpDetail(w, 500, err.Error())
		return
	}
	var data any
	if jerr := json.Unmarshal(res.Data, &data); jerr != nil {
		writeJSON(w, 200, json.RawMessage(res.Data))
		return
	}
	writeJSON(w, 200, v2MaskLegacy(r, data, false))
}

type jmChapterInfo struct {
	PhotoID            string
	AlbumID            string
	ScrambleID         string
	DataOriginalDomain any
	RawImages          []string
	Names              []string
	Title              string
	Index              int
}

func fetchJmChapter(ctx context.Context, ck map[string]string, photoID string) (*jmChapterInfo, error) {
	pd, err := apiClient().Chapter(ctx, photoID, ck)
	if err != nil {
		return nil, err
	}
	info := &jmChapterInfo{
		PhotoID:            photoID,
		DataOriginalDomain: pd.DataOriginalDomain,
		Title:              pd.Name,
		RawImages:          []string{},
		Names:              []string{},
	}
	for _, x := range pd.Images {
		if x == "" {
			continue
		}
		s := x
		if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
			if u, uerr := url.Parse(s); uerr == nil {
				s = lastSeg(u.Path)
			} else {
				s = lastSeg(s)
			}
		} else {
			s = lastSeg(s)
		}
		info.RawImages = append(info.RawImages, x)
		info.Names = append(info.Names, s)
	}
	sc := pd.ScrambleID.String()
	dom := pd.DataOriginalDomain
	// JM /chapter API 常不返回 scramble_id / data_original_domain，
	// 需回退 /chapter_view_template 提取（对齐 legacy handleChapter 与 Python 官方实现）。
	if sc == "" || sc == "0" || dom == nil || dom == "" {
		if tpl, terr := apiClient().ChapterViewTemplate(ctx, photoID, ck); terr == nil {
			tplInfo := parseChapterViewTemplate(string(tpl))
			if (sc == "" || sc == "0") && tplInfo["scramble_id"] != nil {
				if s, ok := tplInfo["scramble_id"].(string); ok && s != "" && s != "0" {
					sc = s
				}
			}
			if (dom == nil || dom == "") && tplInfo["data_original_domain"] != nil {
				dom = tplInfo["data_original_domain"]
			}
		}
	}
	if sc == "" || sc == "0" {
		sc = "0"
	}
	info.ScrambleID = sc
	info.DataOriginalDomain = dom
	aid := pd.SeriesID
	if aid == "" || aid == "0" {
		// JM 接口对单话漫画返回 series_id="0"，需回退到章节自身 ID 作为 album_id
		aid = pd.ID.String()
	}
	info.AlbumID = aid
	sortV := toInt(pd.Sort)
	idx := sortV
	if (pd.SeriesID == "" || pd.SeriesID == "0") && sortV == 2 {
		idx = 1
	}
	info.Index = idx
	return info, nil
}

func chapterInfoMap(info *jmChapterInfo, photoID string) map[string]any {
	return map[string]any{
		"photo_id":             photoID,
		"album_id":             info.AlbumID,
		"scramble_id":          info.ScrambleID,
		"data_original_domain": info.DataOriginalDomain,
		"images":               info.Names,
		"title":                info.Title,
		"index":                info.Index,
	}
}

func favoritesNotLoginFallback() map[string]any {
	return map[string]any{
		"content": []any{},
		"total":   0,
		"pages":   1,
		"folders": []any{},
		"st":      StatusNotLogin,
		"msg":     "Not logged in",
	}
}

func handleFavorites(w http.ResponseWriter, r *http.Request) {
	page := atoiDefault(r.URL.Query().Get("page"), 1)
	fid := r.URL.Query().Get("folder_id")
	if fid == "" {
		fid = "0"
	}
	identity := effIdentityOf(r)
	run := func() (map[string]any, error) {
		ck := store.LoadCookies(identity)
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		res, ferr := apiClient().APIGet(ctx, "/favorite", favoriteQuery(page, fid), ck)
		if ferr != nil {
			return nil, ferr
		}
		data := adaptFavorites(decodeAny(res.Data))
		ids := []string{}
		if content, okc := data["content"].([]map[string]any); okc {
			for _, it := range content {
				if aid := pyStr(it["album_id"]); aid != "" {
					ids = append(ids, aid)
				}
			}
		}
		if len(ids) > 0 {
			store.JmAddFavoriteIDs(identity, ids...)
		}
		return mergeOK(data, ""), nil
	}
	data, rerr := run()
	if rerr == nil {
		writeJSON(w, 200, data)
		return
	}
	if is401Err(rerr) {
		if reloginFromSavedConfig(r) {
			data2, rerr2 := run()
			if rerr2 == nil {
				writeJSON(w, 200, data2)
				return
			}
		}
		writeJSON(w, 200, favoritesNotLoginFallback())
		return
	}
	httpDetail(w, 500, rerr.Error())
}

func handleFavoriteFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type       string `json:"type"`
		FolderID   string `json:"folder_id"`
		FolderName string `json:"folder_name"`
		AlbumID    string `json:"album_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	t := strings.ToLower(strings.TrimSpace(body.Type))
	identity := effIdentityOf(r)
	run := func() (map[string]any, error) {
		switch t {
		case "add":
			name := strings.TrimSpace(body.FolderName)
			if name == "" {
				return errSt(StatusUserError, "Missing folder_name"), nil
			}
			ck := store.LoadCookies(identity)
			form := url.Values{}
			form.Set("folder_name", name)
			form.Set("type", "add")
			if aid := strings.TrimSpace(body.AlbumID); aid != "" {
				form.Set("aid", aid)
			}
			raw, oerr := folderOp(ck, form)
			if oerr != nil {
				return nil, oerr
			}
			folders, lastMsg, matched, verr := verifyFolders(4, ck, func(fl []map[string]string) bool {
				for _, f := range fl {
					if f["name"] == name {
						return true
					}
				}
				return false
			})
			if verr != nil {
				return nil, verr
			}
			if matched {
				return mergeOK(map[string]any{"result": raw, "folders": folders}, ""), nil
			}
			return errData(StatusError, "Folder add not applied", map[string]any{"result": raw, "folders": folders, "error": lastMsg}), nil
		case "del":
			fid := strings.TrimSpace(body.FolderID)
			if fid == "" || fid == "0" {
				return errSt(StatusUserError, "Invalid folder_id"), nil
			}
			ck := store.LoadCookies(identity)
			form := url.Values{}
			form.Set("folder_id", fid)
			form.Set("type", "del")
			raw, oerr := folderOp(ck, form)
			if oerr != nil {
				return nil, oerr
			}
			folders, lastMsg, matched, verr := verifyFolders(4, ck, func(fl []map[string]string) bool {
				return findFolder(fl, fid) == nil
			})
			if verr != nil {
				return nil, verr
			}
			if matched {
				return mergeOK(map[string]any{"result": raw, "folders": folders}, ""), nil
			}
			return errData(StatusError, "Folder delete not applied", map[string]any{"result": raw, "folders": folders, "error": lastMsg}), nil
		case "rename":
			fid := body.FolderID
			name := body.FolderName
			ck := store.LoadCookies(identity)
			form := url.Values{}
			form.Set("folder_id", fid)
			form.Set("folder_name", name)
			form.Set("type", "rename")
			raw, oerr := folderOp(ck, form)
			if oerr != nil {
				return nil, oerr
			}
			if isFailStatus(raw) {
				form2 := url.Values{}
				form2.Set("folder_id", fid)
				form2.Set("folder_name", name)
				form2.Set("type", "edit")
				raw2, oerr2 := folderOp(ck, form2)
				if oerr2 != nil {
					return nil, oerr2
				}
				if !isFailStatus(raw2) {
					raw = raw2
				}
			}
			fid0 := strings.TrimSpace(fid)
			name0 := strings.TrimSpace(name)
			folders, lastMsg, matched, verr := verifyFolders(4, ck, func(fl []map[string]string) bool {
				f := findFolder(fl, fid0)
				return f != nil && f["name"] == name0
			})
			if verr != nil {
				return nil, verr
			}
			if matched {
				return mergeOK(map[string]any{"result": raw, "folders": folders}, ""), nil
			}
			if fid0 == "" || fid0 == "0" || name0 == "" {
				return errData(StatusUserError, "Invalid folder_id or folder_name", map[string]any{"result": raw, "folders": folders}), nil
			}
			addForm := url.Values{}
			addForm.Set("folder_name", name0)
			addForm.Set("type", "add")
			emuAddRaw, oerr3 := folderOp(ck, addForm)
			if oerr3 != nil {
				return nil, oerr3
			}
			newFID := ""
			folders2, lastMsg2, matched2, verr2 := verifyFolders(4, ck, func(fl []map[string]string) bool {
				matches := []map[string]string{}
				for _, f := range fl {
					if f["name"] == name0 && f["id"] != fid0 {
						matches = append(matches, f)
					}
				}
				if len(matches) == 0 {
					return false
				}
				sort.Slice(matches, func(i, j int) bool {
					return asIntID(matches[i]["id"]) < asIntID(matches[j]["id"])
				})
				newFID = matches[len(matches)-1]["id"]
				return true
			})
			if verr2 != nil {
				return nil, verr2
			}
			if !matched2 || newFID == "" {
				errPayload := lastMsg2
				if errPayload == "" {
					errPayload = lastMsg
				}
				return errData(StatusError, "Folder rename failed and fallback add not applied", map[string]any{"result": raw, "add_result": emuAddRaw, "folders": folders2, "error": errPayload}), nil
			}
			merr := func() error {
				fq := url.Values{}
				fq.Set("page", "1")
				fq.Set("folder_id", fid0)
				fq.Set("o", "mr")
				ctxF, cancelF := context.WithTimeout(context.Background(), 6*time.Second)
				defer cancelF()
				resF, ferr := apiClient().APIGet(ctxF, "/favorite", fq, ck)
				if ferr != nil {
					return ferr
				}
				dFirst := adaptFavorites(decodeAny(resF.Data))
				total := toInt(dFirst["total"])
				if total > 200 {
					return &folderTooLarge{total: total}
				}
				oldPage := 1
				moved := 0
				maxMoves := 220
				for moved < maxMoves {
					var dF map[string]any
					if oldPage == 1 {
						dF = dFirst
					} else {
						fp := url.Values{}
						fp.Set("page", strconv.Itoa(oldPage))
						fp.Set("folder_id", fid0)
						fp.Set("o", "mr")
						ctxP, cancelP := context.WithTimeout(context.Background(), 6*time.Second)
						resP, ferr := apiClient().APIGet(ctxP, "/favorite", fp, ck)
						cancelP()
						if ferr != nil {
							return ferr
						}
						dF = adaptFavorites(decodeAny(resP.Data))
					}
					items, _ := dF["content"].([]map[string]any)
					if len(items) == 0 {
						break
					}
					for _, it := range items {
						if moved >= maxMoves {
							break
						}
						aid := strings.TrimSpace(pyStr(it["album_id"]))
						if aid == "" {
							continue
						}
						mv := url.Values{}
						mv.Set("folder_id", newFID)
						mv.Set("type", "move")
						mv.Set("aid", aid)
						if _, merr2 := folderOp(ck, mv); merr2 != nil {
							return merr2
						}
						moved++
					}
					pages := toInt(dF["pages"])
					if oldPage >= pages {
						break
					}
					oldPage++
				}
				delForm := url.Values{}
				delForm.Set("folder_id", fid0)
				delForm.Set("type", "del")
				if _, derr := folderOp(ck, delForm); derr != nil {
					return derr
				}
				return nil
			}()
			if merr != nil {
				if tle, okTl := merr.(*folderTooLarge); okTl {
					return errData(StatusError, "Folder too large to migrate automatically", map[string]any{"result": raw, "new_folder_id": newFID, "total": tle.total}), nil
				}
				return errData(StatusError, "Folder rename fallback move failed", map[string]any{"result": raw, "new_folder_id": newFID, "error": merr.Error()}), nil
			}
			folders3, lastMsg3, matched3, verr3 := verifyFolders(6, ck, func(fl []map[string]string) bool {
				return findFolder(fl, fid0) == nil && findFolder(fl, newFID) != nil
			})
			if verr3 != nil {
				return nil, verr3
			}
			if matched3 {
				return mergeOK(map[string]any{"result": raw, "folders": folders3, "emulated": true, "old_folder_id": fid0, "new_folder_id": newFID}, ""), nil
			}
			return errData(StatusError, "Folder rename fallback not fully applied", map[string]any{"result": raw, "new_folder_id": newFID, "folders": folders3, "error": lastMsg3}), nil
		case "move":
			ck := store.LoadCookies(identity)
			form := url.Values{}
			form.Set("folder_id", body.FolderID)
			form.Set("type", "move")
			form.Set("aid", body.AlbumID)
			raw, oerr := folderOp(ck, form)
			if oerr != nil {
				return nil, oerr
			}
			return mergeOK(map[string]any{"result": raw}, ""), nil
		default:
			return errSt(StatusUserError, "Invalid type"), nil
		}
	}
	data, rerr := run()
	if rerr == nil {
		writeJSON(w, 200, data)
		return
	}
	if is401Err(rerr) {
		if reloginFromSavedConfig(r) {
			data2, rerr2 := run()
			if rerr2 == nil {
				writeJSON(w, 200, data2)
				return
			}
		}
		writeJSON(w, 200, errSt(StatusNotLogin, "Not logged in"))
		return
	}
	writeJSON(w, 200, errSt(StatusError, rerr.Error()))
}
