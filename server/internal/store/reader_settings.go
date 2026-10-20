package store

// 阅读器设置（保留上 N 页等）按「站点用户」保存在 aura_library.json 的
// users.<user>.reader_settings 下——和阅读历史同一个文件，备份/迁移只需搬一个文件。
//
// 为什么不复用前端 localStorage：localStorage 只存在于单台设备/浏览器，
// 换设备或清缓存就丢；阅读习惯（尤其是保留页数这种和内存直接相关的偏好）应当跟着账号走。

// 白名单键：只存这些，避免前端传任意键把文件撑大。
var readerSettingKeys = []string{"width", "gap", "initial", "batch", "keep_pages"}

// AuraGetReaderSettings 返回该用户的阅读器设置；从未保存过时返回 nil（前端用本地默认值）。
func AuraGetReaderSettings(user string) map[string]any {
	doc := loadAuraDoc()
	b := auraBucket(doc, user)
	s, _ := b["reader_settings"].(map[string]any)
	if len(s) == 0 {
		return nil
	}
	out := make(map[string]any, len(s))
	for k, v := range s {
		out[k] = v
	}
	return out
}

// AuraSaveReaderSettings 覆盖式保存该用户的阅读器设置。
func AuraSaveReaderSettings(user string, settings map[string]any) {
	doc := loadAuraDoc()
	b := auraBucket(doc, user)
	rec := make(map[string]any, len(readerSettingKeys)+1)
	for _, k := range readerSettingKeys {
		if v, exists := settings[k]; exists {
			rec[k] = v
		}
	}
	rec["updated_at"] = nowMS()
	b["reader_settings"] = rec
	saveAuraDoc(doc)
}
