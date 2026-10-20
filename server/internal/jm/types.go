package jm

import (
	"encoding/json"
	"strconv"
	"strings"
)

type flexFloat float64

func (f *flexFloat) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*f = flexFloat(v)
	return nil
}

type flexInt int

func (i *flexInt) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*i = flexInt(v)
	return nil
}

type Envelope struct {
	Code     int             `json:"code"`
	Data     json.RawMessage `json:"data"`
	ErrorMsg string          `json:"errorMsg"`
}

type CategoryRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type SearchItem struct {
	ID          string      `json:"id"`
	Author      string      `json:"author"`
	Description string      `json:"description"`
	Name        string      `json:"name"`
	Image       string      `json:"image"`
	Category    CategoryRef `json:"category"`
	CategorySub CategoryRef `json:"category_sub"`
	Tags        []string    `json:"tags"`
}

type SearchPage struct {
	SearchQuery string       `json:"search_query"`
	Total       json.Number  `json:"total"`
	RedirectAid string       `json:"redirect_aid"`
	Content     []SearchItem `json:"content"`
}

type FolderEntry struct {
	FID  string `json:"FID"`
	UID  string `json:"UID"`
	Name string `json:"name"`
}

type FavoriteItem struct {
	ID          string      `json:"id"`
	Author      string      `json:"author"`
	Description string      `json:"description"`
	Name        string      `json:"name"`
	Image       string      `json:"image"`
	Category    CategoryRef `json:"category"`
	CategorySub CategoryRef `json:"category_sub"`
	LatestEp    *string     `json:"latest_ep"`
	LatestEpAid *string     `json:"latest_ep_aid"`
}

type FavoritePage struct {
	List       []FavoriteItem `json:"list"`
	FolderList []FolderEntry  `json:"folder_list"`
	Total      json.Number    `json:"total"`
	Count      int            `json:"count"`
}

type SeriesEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Sort string `json:"sort"`
}

type AlbumData struct {
	ID           json.Number   `json:"id"`
	Name         string        `json:"name"`
	Author       []string      `json:"author"`
	Description  *string       `json:"description"`
	TotalViews   string        `json:"total_views"`
	Likes        string        `json:"likes"`
	Series       []SeriesEntry `json:"series"`
	SeriesID     string        `json:"series_id"`
	CommentTotal string        `json:"comment_total"`
	Tags         []string      `json:"tags"`
	Works        []string      `json:"works"`
	Actors       []string      `json:"actors"`
	RelatedList  []SearchItem  `json:"related_list"`
	Liked        bool          `json:"liked"`
	IsFavorite   bool          `json:"is_favorite"`
}

type PhotoData struct {
	ID         json.Number   `json:"id"`
	Name       string        `json:"name"`
	Sort       json.Number   `json:"sort"`
	Tags       string        `json:"tags"`
	Images     []string      `json:"images"`
	SeriesID   string        `json:"series_id"`
	Series     []SeriesEntry `json:"series"`
	IsFavorite bool          `json:"is_favorite"`
	Liked      bool          `json:"liked"`

	ScrambleID         json.Number `json:"scramble_id"`
	DataOriginalDomain any         `json:"data_original_domain"`
}

type LoginResult struct {
	UID               string    `json:"uid"`
	Username          string    `json:"username"`
	Email             string    `json:"email"`
	Photo             string    `json:"photo"`
	Gender            string    `json:"gender"`
	Message           string    `json:"message"`
	Coin              flexFloat `json:"coin"`
	AlbumFavorites    flexInt   `json:"album_favorites"`
	S                 string    `json:"s"`
	LevelName         string    `json:"level_name"`
	Level             flexInt   `json:"level"`
	NextLevelExp      flexInt   `json:"nextLevelExp"`
	Exp               string    `json:"exp"`
	ExpPercent        flexFloat `json:"expPercent"`
	AlbumFavoritesMax flexInt   `json:"album_favorites_max"`
}

type CommentData struct {
	CID       json.Number   `json:"CID"`
	AID       string        `json:"AID"`
	UID       string        `json:"UID"`
	ParentCID json.Number   `json:"parent_CID"`
	Content   string        `json:"content"`
	Username  string        `json:"username"`
	Nickname  string        `json:"nickname"`
	Likes     int           `json:"likes"`
	AddTime   string        `json:"addtime"`
	IsSpoiler interface{}   `json:"is_spoiler"`
	Replys    []CommentData `json:"replys"`
	Photo     string        `json:"photo"`
}

type CommentPage struct {
	List  []CommentData `json:"list"`
	Total json.Number   `json:"total"`
}

type StatusMessage struct {
	Status string `json:"status"`
	Msg    string `json:"msg"`
}

type NovelItem struct {
	ID          string      `json:"id"`
	Author      string      `json:"author"`
	Description string      `json:"description"`
	Name        string      `json:"name"`
	Image       string      `json:"image"`
	Category    CategoryRef `json:"category"`
	CategorySub CategoryRef `json:"category_sub"`
	Tags        []string    `json:"tags"`
}

type NovelPage struct {
	SearchQuery string      `json:"search_query"`
	Total       json.Number `json:"total"`
	RedirectAid string      `json:"redirect_aid"`
	Content     []NovelItem `json:"content"`
}

type NovelSeriesEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Sort string `json:"sort"`
}

type NovelData struct {
	ID           json.Number        `json:"id"`
	Name         string             `json:"name"`
	Author       []string           `json:"author"`
	Description  *string            `json:"description"`
	Tags         []string           `json:"tags"`
	Series       []NovelSeriesEntry `json:"series"`
	SeriesID     string             `json:"series_id"`
	CommentTotal string             `json:"comment_total"`
	Liked        bool               `json:"liked"`
	IsFavorite   bool               `json:"is_favorite"`
}

type NovelChapterData struct {
	ID      json.Number `json:"id"`
	Title   string      `json:"title"`
	Content string      `json:"content"`
	Sort    json.Number `json:"sort"`
}
