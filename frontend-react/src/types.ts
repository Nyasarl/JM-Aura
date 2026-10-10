// 与 Go 后端 v2.go / content.go / aura.go 响应结构对齐的共享类型。

export interface ComicSummary {
  source: string
  comic_id: string
  title: string
  author?: string | null
  cover_url?: string | null
  tags: string[]
  category?: string | null
}

export interface ChapterSummary {
  id: string
  title: string
  order: number
}

export interface ComicDetail extends ComicSummary {
  description?: string | null
  is_favorite?: boolean
  chapters: ChapterSummary[]
  /** 命中屏蔽规则且开启了「详情页遮挡」时由后端置位。 */
  masked?: boolean
  masked_reason?: string
}

export interface ChapterPage {
  name?: string | null
  url?: string | null
}

export interface ChapterRaw {
  photo_id?: string
  album_id?: string
  scramble_id?: string
  data_original_domain?: string | null
  images?: string[]
  title?: string
  index?: number
}

export interface ChapterDetail {
  source: string
  chapter_id: string
  title?: string | null
  images: ChapterPage[]
  raw: ChapterRaw
}

// ── 小说 ──

export interface NovelSummary {
  source: string
  novel_id: string
  title: string
  author?: string | null
  cover_url?: string | null
  category?: string | null
}

export interface NovelChapterSummary {
  id: string
  title: string
  order: number
}

export interface NovelDetail {
  source: string
  novel_id: string
  title: string
  author?: string | null
  cover_url?: string | null
  description?: string | null
  tags: string[]
  category?: string | null
  is_favorite?: boolean | null
  chapters: NovelChapterSummary[]
}

export interface NovelChapterDetail {
  source: string
  chapter_id: string
  title?: string | null
  content: string
}

/** 小说选话导出任务（后端 novel_dl.go toPublic 对齐）。 */
export interface NovelExportTask {
  task_id: string
  novel_id: string
  novel_title: string
  author?: string | null
  lang: string
  format: string
  status: string
  stage: string
  message: string
  total_chapters: number
  downloaded_chapters: number
  failed_chapters: number
  percent: number
  download_url?: string
  /** 仅排队中的任务有：前面还有多少个任务在等。 */
  queued_ahead?: number
}

/** 漫画选话下载任务（后端 dl.go toPublic 对齐）。 */
export interface ComicDownloadTask {
  task_id: string
  album_id: string
  album_title: string
  status: string
  stage: string
  message: string
  total_images: number
  downloaded_images: number
  total_zip_files: number
  zipped_files: number
  percent: number
  /** true=无损 PNG 打包，false/缺省=JPEG 默认打包。 */
  lossless?: boolean
  download_url?: string
}

export interface SiteMe {
  username: string
  is_admin: boolean
}

// ── 社区（原「阅读笔记」，app/recommend.go 响应结构对齐） ──

/** 作品类型：后端自动识别，决定封面目录与详情页跳转。 */
export type WorkKind = 'comic' | 'novel'

export interface Recommendation {
  id: string
  /** 关联作品 id；纯说说帖为空字符串。 */
  comic_id: string
  comic_title: string
  kind: WorkKind
  /** 作品所属源；老数据缺省为 jm。 */
  source?: 'jm' | 'bika'
  body: string
  author: string
  author_name: string
  created_at: number
  cover_url: string
  /** 帖子引用的外链图片（服务端只存链接，不接受上传）；老数据缺省为空数组。 */
  images: string[]
  like_count: number
  comment_count: number
  liked_by_me: boolean
  can_delete: boolean
}

export interface RecommendationList {
  items: Recommendation[]
  total: number
  page: number
  page_size: number
  has_more: boolean
  is_moderator: boolean
}

/** 一条帖子评论。 */
export interface RecommendComment {
  id: string
  body: string
  author: string
  author_name: string
  created_at: number
  can_delete: boolean
}

export interface RecommendCommentList {
  items: RecommendComment[]
  total: number
  page: number
  page_size: number
  has_more: boolean
}

export interface V2Comment {
  CID?: number | string
  nickname?: string
  username?: string
  content?: string
  created_at?: string
  likes?: number | string
  spoiler?: string
  replys?: V2Comment[]
  children?: V2Comment[]
  [key: string]: unknown
}

export interface V2UserProfile {
  source: string
  username?: string | null
  nickname?: string | null
  avatar_url?: string | null
  signature?: string | null
  raw?: Record<string, unknown>
}

/** 原图代理地址（同源，供阅读器在本机 canvas 还原分块乱序图）。
 *  domain 参数对齐旧版 Vue 实现，让后端优先用 chapter_view_template 返回的域名拉图。
 *  默认不带 scramble：服务端原样转发，乱序还原由用户设备完成，服务端零解码开销。 */
export function chapterImageUrl(photoId: string, imageName: string, domain?: string | null): string {
  let url = `/api/chapter_image/${encodeURIComponent(photoId)}/${encodeURIComponent(imageName)}`
  if (domain) {
    url += `?domain=${encodeURIComponent(domain)}`
  }
  return url
}

/** 服务端还原地址：仅在本机 canvas 还原失败时作为兜底通道使用。
 *  带 scramble 参数时后端才做解码 + 分块反置乱，因此绝大多数请求都不会走这里。 */
export function chapterImageServerUrl(
  photoId: string,
  imageName: string,
  domain?: string | null,
  scrambleId?: string | null,
): string {
  let url = `/api/chapter_image/${encodeURIComponent(photoId)}/${encodeURIComponent(imageName)}`
  const q: string[] = []
  if (domain) q.push(`domain=${encodeURIComponent(domain)}`)
  const sc = String(scrambleId ?? '').trim()
  if (sc && sc !== '0') q.push(`scramble=${encodeURIComponent(sc)}`)
  if (q.length) url += `?${q.join('&')}`
  return url
}
