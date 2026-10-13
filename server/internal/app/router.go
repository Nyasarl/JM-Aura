package app

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:webdist
var webDistFS embed.FS

func route(mux *http.ServeMux, method, pattern string, h http.HandlerFunc) {
	mux.HandleFunc(method+" "+pattern, h)
}

// NewRouter wires every endpoint. Legacy paths mirror backend/main.py
// decorators one-to-one; aura extension routes use fresh RESTful paths;
// v2 provider routes mirror the /api/v2 surface exactly.
func NewRouter() *http.ServeMux {
	mux := http.NewServeMux()

	route(mux, http.MethodPost, "/api/site/login", handleSiteLogin)
	route(mux, http.MethodPost, "/api/site/logout", handleSiteLogout)
	route(mux, http.MethodGet, "/api/site/me", handleSiteMe)
	route(mux, http.MethodGet, "/api/site/online", handleSiteOnline)

	route(mux, http.MethodGet, "/api/aura/library/history", handleAuraHistoryGet)
	route(mux, http.MethodPost, "/api/aura/library/history", handleAuraHistoryPost)

	route(mux, http.MethodGet, "/api/recommend", handleRecommendList)
	route(mux, http.MethodPost, "/api/recommend", handleRecommendCreate)
	route(mux, http.MethodDelete, "/api/recommend/{id}", handleRecommendDelete)
	route(mux, http.MethodPost, "/api/recommend/{id}/like", handleRecommendLike)
	route(mux, http.MethodGet, "/api/recommend/{id}/comments", handleRecommendComments)
	route(mux, http.MethodPost, "/api/recommend/{id}/comments", handleRecommendCommentCreate)
	route(mux, http.MethodDelete, "/api/recommend/comments/{id}", handleRecommendCommentDelete)

	route(mux, http.MethodPost, "/api/config", handleConfigPost)
	route(mux, http.MethodGet, "/api/config", handleConfigGet)
	route(mux, http.MethodGet, "/api/announcement", handleAnnouncementGet)
	route(mux, http.MethodGet, "/api/credentials", handleCredentialsGet)
	route(mux, http.MethodDelete, "/api/credentials", handleCredentialsDelete)
	route(mux, http.MethodPost, "/api/session/relogin", handleSessionRelogin)

	route(mux, http.MethodGet, "/api/promote", handlePromote)
	route(mux, http.MethodGet, "/api/afdian/sponsors", handleAfdianSponsors)
	route(mux, http.MethodGet, "/api/afdian/binding", handleAfdianBindingGet)
	route(mux, http.MethodPost, "/api/afdian/binding", handleAfdianBindingPost)
	route(mux, http.MethodGet, "/api/latest", handleLatest)
	route(mux, http.MethodGet, "/api/favorites", handleFavorites)
	route(mux, http.MethodPost, "/api/favorite_folder", handleFavoriteFolder)

	route(mux, http.MethodGet, "/api/image-proxy", handleImageProxy)
	route(mux, http.MethodGet, "/api/chapter_image/{photo_id}/{image_name}", handleChapterImage)

	route(mux, http.MethodPost, "/api/v2/{source}/auth/login", handleV2Login)
	route(mux, http.MethodPost, "/api/v2/{source}/auth/logout", handleV2Logout)
	route(mux, http.MethodPost, "/api/v2/{source}/auth/register", handleV2Register)
	route(mux, http.MethodGet, "/api/v2/{source}/auth/register/{task_id}", handleV2RegisterStatus)
	route(mux, http.MethodGet, "/api/v2/{source}/capabilities", handleV2Capabilities)
	route(mux, http.MethodGet, "/api/v2/{source}/user/profile", handleV2ProfileGet)
	route(mux, http.MethodPost, "/api/v2/{source}/user/checkin", handleV2Checkin)
	route(mux, http.MethodPut, "/api/v2/{source}/user/profile", handleV2ProfileUpdate)
	route(mux, http.MethodPut, "/api/v2/{source}/user/password", handleV2PasswordUpdate)
	route(mux, http.MethodPut, "/api/v2/{source}/user/avatar", handleV2AvatarUpdate)
	route(mux, http.MethodGet, "/api/v2/{source}/categories", handleV2Categories)
	route(mux, http.MethodGet, "/api/v2/{source}/filter", handleV2FilterGet)
	route(mux, http.MethodPut, "/api/v2/{source}/filter", handleV2FilterPut)
	route(mux, http.MethodPost, "/api/v2/{source}/filter/block", handleV2FilterBlock)
	route(mux, http.MethodGet, "/api/v2/{source}/search", handleV2Search)
	route(mux, http.MethodGet, "/api/v2/{source}/leaderboard", handleV2Leaderboard)
	route(mux, http.MethodGet, "/api/v2/{source}/random", handleV2Random)
	route(mux, http.MethodGet, "/api/v2/{source}/daily", handleV2Daily)
	route(mux, http.MethodGet, "/api/v2/{source}/also_viewed/{comic_id}", handleV2AlsoViewed)
	route(mux, http.MethodGet, "/api/v2/{source}/comic/{comic_id}", handleV2ComicDetail)
	route(mux, http.MethodGet, "/api/v2/{source}/chapter/{chapter_id}", handleV2ChapterDetail)
	route(mux, http.MethodGet, "/api/v2/{source}/comic/{comic_id}/comments", handleV2Comments)
	route(mux, http.MethodPost, "/api/v2/{source}/comic/{comic_id}/comments", handleV2SendComment)
	route(mux, http.MethodPost, "/api/v2/{source}/comment/{comment_id}/like", handleV2LikeComment)
	route(mux, http.MethodGet, "/api/v2/{source}/favorites", handleV2Favorites)
	route(mux, http.MethodPost, "/api/v2/{source}/comic/{comic_id}/favorite", handleV2ToggleFavorite)
	route(mux, http.MethodPost, "/api/v2/{source}/comic/{comic_id}/like", handleV2LikeComic)

	route(mux, http.MethodGet, "/api/v2/{source}/novels", handleV2NovelList)
	route(mux, http.MethodGet, "/api/v2/{source}/novel/search", handleV2NovelSearch)
	route(mux, http.MethodGet, "/api/v2/{source}/novel_favorites", handleV2NovelFavorites)
	route(mux, http.MethodGet, "/api/v2/{source}/novel/{novel_id}", handleV2NovelDetail)
	route(mux, http.MethodGet, "/api/v2/{source}/novel_chapter/{chapter_id}", handleV2NovelChapter)
	route(mux, http.MethodPost, "/api/v2/{source}/novel/{novel_id}/favorite", handleV2NovelToggleFavorite)
	route(mux, http.MethodPost, "/api/v2/{source}/novel/{novel_id}/favorite_folder", handleV2NovelFavoriteFolder)
	route(mux, http.MethodPost, "/api/v2/{source}/novel/{novel_id}/like", handleV2NovelLike)
	route(mux, http.MethodGet, "/api/v2/{source}/novel/{novel_id}/comments", handleV2NovelComments)
	route(mux, http.MethodPost, "/api/v2/{source}/novel/{novel_id}/comments", handleV2NovelSendComment)

	route(mux, http.MethodPost, "/api/v2/{source}/download/tasks", handleV2CreateDownloadTask)
	route(mux, http.MethodGet, "/api/v2/{source}/download/tasks/{task_id}", handleV2GetDownloadTask)
	route(mux, http.MethodDelete, "/api/v2/{source}/download/tasks/{task_id}", handleV2CancelDownloadTask)
	route(mux, http.MethodGet, "/api/v2/{source}/download/tasks/{task_id}/download", handleV2DownloadTaskZip)

	route(mux, http.MethodPost, "/api/v2/{source}/novel/export/tasks", handleV2NovelExport)
	route(mux, http.MethodGet, "/api/v2/{source}/novel/export/tasks/{task_id}", handleV2GetNovelExport)
	route(mux, http.MethodDelete, "/api/v2/{source}/novel/export/tasks/{task_id}", handleV2CancelNovelExport)
	route(mux, http.MethodGet, "/api/v2/{source}/novel/export/tasks/{task_id}/download", handleV2NovelExportDownload)

	route(mux, http.MethodPost, "/api/v2/cache/cleanup", handleV2CacheCleanup)
	route(mux, http.MethodGet, "/api/v2/cache/stats", handleV2CacheStats)

	registerSPA(mux)
	return mux
}

// registerSPA serves the embedded frontend: real files win, extension-less
// paths fall back to index.html (history-mode routing), unknown assets 404.
func registerSPA(mux *http.ServeMux) {
	sub, serr := fs.Sub(webDistFS, "webdist")
	if serr != nil {
		panic(serr)
	}
	index, ierr := fs.ReadFile(sub, "index.html")
	if ierr != nil {
		panic(ierr)
	}
	files := http.FileServer(http.FS(sub))
	writeIndex := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(index)
	}
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			writeIndex(w)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			httpDetail(w, http.StatusNotFound, "Not Found")
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" || strings.HasPrefix(p, "..") {
			httpDetail(w, http.StatusNotFound, "Not Found")
			return
		}
		if st, statErr := fs.Stat(sub, p); statErr == nil && !st.IsDir() {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		if !strings.Contains(path.Base(p), ".") {
			writeIndex(w)
			return
		}
		httpDetail(w, http.StatusNotFound, "Not Found")
	})
}
