<div align="center">
  <img src="https://blog.2z2.org/upload/jm-aura5.png" alt="JM-Aura" width="230" height="230" />

  <h1><i>JM-Aura</i></h1>
  <p><i>一个简洁、优雅的 JMComic 漫画阅读/下载 Web 应用 · Go 重构版 · 单二进制部署</i></p>

  [![GitHub](https://img.shields.io/badge/-GitHub-181717?logo=github)](https://github.com/Tom6814)
  [![GitHub license](https://img.shields.io/github/license/Tom6814/JM-Aura)](https://github.com/Tom6814/JM-Aura/blob/master/LICENSE)
  [![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
  [![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=white)](https://react.dev/)
</div>

---

# 这是什么？

**JM-Aura** 是一个面向 **JMComic** 的本地/自建 Web 应用：
你只需要启动一个后端进程，然后用浏览器打开 `<服务器IP>:8000`（默认），就能完成 **搜索、浏览、收藏、历史、阅读、批量下载与打包 JMComic 漫画** 等操作。

项目已从早期的 Python (FastAPI + Vue3 CDN) 全面重构为 **Go + React 19** 单二进制架构：

- **后端**：Go 1.25，GORM 数据库，`go:embed` 将前端构建产物直接嵌入二进制，部署只需一个可执行文件。
- **前端**：React 19 + MUI 7（Material Design 3），Vite 7 构建，TypeScript 编写。
- **运行态存储**：GORM 数据库（下载任务记录）+ 若干 JSON 文件（Cookie、凭据、影子账号、Aura 历史等）。

> 整体架构图：
> ```
> ┌─────────────────────────────────────────────┐
> │              单二进制 jmaura                 │
> │  ┌───────────────┐   ┌───────────────────┐  │
> │  │  Go HTTP 服务  │   │ embed.FS 前端产物  │  │
> │  │  (net/http)    │──▶│ (webdist/*)       │  │
> │  │  /api/* 路由   │   │ SPA + history 路由 │  │
> │  └───────┬───────┘   └───────────────────┘  │
> │          │                                  │
> │   ┌──────▼──────┐   ┌──────────────────┐   │
> │   │  GORM 数据库 │   │  JSON 文件存储    │   │
> │   │ (下载任务)   │   │ (Cookie/凭据/...) │   │
> │   └─────────────┘   └──────────────────┘   │
> │          │                                  │
> │   ┌──────▼──────────────────────────────┐  │
> │   │  JM Client (域名容错 + AES解密 +    │  │
> │   │  图片反 scrambling)                  │  │
> │   └─────────────────────────────────────┘  │
> └─────────────────────────────────────────────┘
> ```

## 它能干嘛？实现了 JMComic 的哪些功能？

- **浏览与搜索**：按关键词搜索标题/作者/标签；按分类、排行、最新、随机浏览。
- **沉浸阅读**：长条漫垂直滚动；阅读器模式自动隐藏顶/底栏，减少干扰。分块乱序图默认由**浏览器本机还原**（服务端零解码开销），仅在本机还原失败时才回退到服务端还原。
- **继续阅读**：记住上次阅读到的章节与页码；登录后会优先从影子账号历史中恢复，不只依赖本地缓存。
- **收藏与历史**：收藏页、历史页独立入口；收藏完全托管于 JM 云端（服务端收藏夹），本地仅作状态缓存；本地历史与影子账号历史并存。
- **下载与打包**：支持选择章节下载（默认 JPEG，可选无损 PNG）；后台异步任务队列，实时进度展示；完成后可直接下载 ZIP；图片自动反 scrambling 还原。**下载为登录用户专属**，任务按串行队列执行（避免占满 CPU），排队中会显示前方任务数；单个账号同时排队的任务数有上限（默认 10，`JM_AURA_DL_MAX_QUEUED` 可调），到达上限只提示等待、任务完成后即可继续提交，不设永久拒绝。**无损 PNG 为爱发电赞助者专享**：在设置页绑定赞助订单号后解锁（一单一账号、绑定一次长期有效）。
- **多账号管理**：每个影子账号可绑定多个 JM 账号，自由切换当前激活账号。
- **网络与线路**：内置多域名容错机制，自动 failover；图片代理服务；域名可从 TOS 动态刷新。
- **账号体系**：JM 登录/注册内置到设置页；登录成功后自动创建本地影子账号并保存会话（PBKDF2 加密，7 天有效期）。
- **社区板块**：原「阅读笔记」升级为社区，帖子是**说说式无标题**内容，可点赞、可评论、可引用外链图片（不支持上传，最多 9 张）。填了作品 ID 的帖子保持原来的「漫画推荐」用法：卡片上显示作品条，点击回到详情页。点赞数 / 评论数 / 我是否点过赞都由服务端一次算好返回，前端只渲染与分页。
- **备注**：为漫画添加标签和笔记。

## 技术栈

| 层        | 技术                                                         |
| --------- | ------------------------------------------------------------ |
| 后端      | Go 1.25 · `net/http` (Go 1.22+ 路由语法) · GORM             |
| 数据库    | SQLite (pure-Go `glebarez/sqlite`) / MySQL / PostgreSQL     |
| 前端      | React 19 · MUI 7 (Material Design 3) · react-router-dom 7   |
| 构建工具  | Vite 7 · TypeScript 5.8                                      |
| 部署      | 单二进制 · Docker 多阶段构建 (Alpine 3.20 运行时)            |
| 加密      | AES-ECB (JM 响应解密) · PBKDF2 (影子账号密码, 200,000 轮)   |

## 响应式导航

前端采用 Material Design 3 的三档断点自适应导航：

```
 窗口宽度        导航形态            说明
 ─────────────────────────────────────────────
 < md (约900px)  底部导航栏         适合手机竖屏
 md ~ lg         导航 Rail (竖条)   适合平板
 ≥ lg            侧边 Drawer        适合桌面，展开完整菜单
```

阅读器模式下 AppBar 自动隐藏，退出阅读器后恢复。

## 🚀 快速部署

### 方式一：Docker（推荐）

最简单的部署方式，一条命令搞定：

```bash
docker build -t jm-aura .

docker run -d \
  --name jm-aura \
  -p 8000:8000 \
  -v jm-aura-data:/data \
  --restart unless-stopped \
  jm-aura
```

浏览器访问 `http://<服务器IP>:8000` 即可。

<details>
<summary>docker-compose 示例</summary>

```yaml
services:
  jm-aura:
    build: .
    ports:
      - "8000:8000"
    volumes:
      - jm-aura-data:/data
    restart: unless-stopped

volumes:
  jm-aura-data:
```

</details>

> Docker 镜像特性：
> - 三阶段构建（Node 22 构建前端 → Go 1.25 构建后端 → Alpine 3.20 运行时）
> - 非 root 用户（uid 10001）运行，仅含 `ca-certificates` + `tzdata`
> - 数据目录固定为 `/data`，挂载 volume 即可持久化
> - 静态编译（`CGO_ENABLED=0`），可跨平台部署

### 方式二：从源码构建

#### 前提条件

- Go 1.25+
- Node.js 22+ (npm)

#### 构建步骤

```bash
# 1. 构建前端（产物直接输出到 server/internal/app/webdist）
cd frontend-react
npm ci
npm run build
cd ..

# 2. 构建后端（前端产物会被 go:embed 嵌入二进制）
cd server
go build -trimpath -ldflags="-s -w" -o jmaura .

# 3. 运行
./jmaura
```

浏览器访问 `http://localhost:8000`。

### 方式三：本地开发（热更新）

开两个终端，前端走 Vite 热更新、后端走 `go run`，Vite 会把 `/api` 请求代理到后端：

```bash
# 终端 1：后端
cd server
go run .

# 终端 2：前端（http://localhost:5173）
cd frontend-react
npm run dev
```

开发时访问 `http://localhost:5173`（非 8000），Vite 会自动代理 API 请求到 `127.0.0.1:8000`。

## 🍽️ 食用方法

### 登录 / 线路

1. 打开页面后进入 **设置（Settings）**。
2. 设置页内可直接进行 **JM 登录 / JM 注册**。
3. 登录成功后，系统会自动创建一个本地影子账号，用来保存：
   - 会话（7 天有效）
   - 设置偏好
   - Aura 历史
   - 继续阅读页码
   - JM 凭据（支持多账号）
4. 如遇到图片加载异常，JM Client 内置多域名容错与自动 failover，会自动尝试切换线路。

### 继续阅读

- 未登录时：只记录到当前设备本地缓存。
- 已登录影子账号时：继续阅读会优先从影子账号历史恢复——上次阅读章节、上次阅读页码。
- 阅读器已优化长章节恢复：恢复到目标页时不会先完整渲染前面所有页，优先渲染目标页附近窗口。

### 多账号管理

- 每个影子账号可绑定多个 JM 账号凭据。
- 在设置页可查看账号列表、切换激活账号、移除账号。
- 激活账号的 Cookie 会自动持久化，下次免登录。

## ⚙️ 配置与文件

### 环境变量

| 变量名                         | 说明                                       | 默认值                     |
| ------------------------------ | ------------------------------------------ | -------------------------- |
| `JM_AURA_HOST`                 | 监听地址                                   | `0.0.0.0`                  |
| `JM_AURA_PORT`                 | 监听端口（也读取 `PORT`）                  | `8000`                     |
| `DATABASE_URL`                 | 数据库连接串（可选，不设则用纯 Go SQLite） | 空 → `DataDir/jmaura.db`   |
| `JM_AURA_DATA_DIR`             | 运行态数据根目录                           | `backend/config` → `config` |
| `JM_AURA_CONFIG_PATH`          | `op.yml` 配置文件路径                      | `config/op.yml`            |
| `JM_AURA_CREDENTIALS_PATH`     | JM 凭据文件路径                            | `DataDir/credentials.json` |
| `JM_AURA_COOKIE_PATH`          | Cookie 存储路径                            | `DataDir/cookies/`         |
| `JM_AURA_SITE_USERS_PATH`      | 影子账号用户文件                           | `DataDir/site_users.json`  |
| `JM_AURA_SITE_SESSIONS_PATH`   | 影子账号会话文件                           | `DataDir/site_sessions.json` |
| `JM_AURA_SITE_PROFILE_PATH`    | 设置页资料文件                             | `DataDir/site_profiles.json` |
| `JM_AURA_AURA_LIBRARY_PATH`    | Aura 历史/备注文件                         | `DataDir/aura_library.json` |
| `JM_AURA_JM_STORE_PATH`        | JM 状态缓存文件                            | `DataDir/jm.json`          |
| `JM_AURA_AFDIAN_USER_ID`       | 爱发电开发者 user_id（赞助名单 / 订单校验） | 空（功能优雅降级）          |
| `JM_AURA_AFDIAN_TOKEN`         | 爱发电开发者 API Token（仅参与签名）        | 空（功能优雅降级）          |
| `JM_AURA_AFDIAN_BINDINGS_PATH` | 爱发电赞助绑定文件                         | `DataDir/afdian_bindings.json` |
| `JM_AURA_DL_CONCURRENCY`       | 下载/导出同时执行的任务数（2 核机器建议保持 1；上限 4） | `1`                     |
| `JM_AURA_DL_MAX_QUEUED`        | 单账号同时排队的下载/导出任务上限（防误刷，非永久拒绝） | `10`                    |

### DATABASE_URL 连接串示例

`DATABASE_URL` **可选**——不设置时自动使用纯 Go SQLite（零依赖、零配置）。如需切换：

```bash
# SQLite（纯 Go 驱动，无需 CGO）
DATABASE_URL=sqlite:///path/to/jmaura.db

# MySQL / MariaDB
DATABASE_URL=mysql://用户名:密码@127.0.0.1:3306/jm_aura?charset=utf8mb4
DATABASE_URL=mariadb://用户名:密码@127.0.0.1:3306/jm_aura

# PostgreSQL
DATABASE_URL=postgres://用户名:密码@127.0.0.1:5432/jm_aura
DATABASE_URL=postgresql://用户名:密码@127.0.0.1:5432/jm_aura
```

> 程序启动时会自动执行 `AutoMigrate` 建表，不需要手动跑 migration。

### 运行态文件说明

以下文件由程序运行时自动生成，可能包含敏感信息，**请不要上传或分享**：

| 文件                        | 内容                                   |
| --------------------------- | -------------------------------------- |
| `op.yml`                    | 运行时线路/配置（可选，自动生成）       |
| `cookies/<user>.json`       | 登录 Cookie（按用户隔离，游客不持久化） |
| `credentials.json`          | JM 凭据（多账号）                       |
| `site_users.json`           | 影子账号用户信息（PBKDF2 加密）         |
| `site_sessions.json`        | 影子账号会话                            |
| `site_profiles.json`        | 设置页资料与偏好                        |
| `aura_library.json`         | Aura 历史 / 备注                        |
| `recommendations.json`      | 社区帖子（说说正文、外链图片、点赞、评论；旧「阅读笔记」数据直接沿用，无需迁移） |
| `jm.json`                   | JM 状态缓存（用户ID、资料、收藏）       |
| `jmaura.db`                 | SQLite 数据库（下载任务记录）           |
| `downloads/`                | 下载产物目录（ZIP 文件）                |
| `downloads/tasks/`          | 下载任务临时图片缓存                    |

> 生产环境请把数据目录加入备份范围。

### 数据目录解析逻辑

程序按以下优先级确定数据目录：

1. `JM_AURA_DATA_DIR` 环境变量（最高优先级，Docker 中固定为 `/data`）
2. `backend/config` 目录（兼容旧版路径）
3. `config` 目录（兜底）

所有 JSON 文件和数据库默认都放在数据目录下。

## 💬 社区板块

原「阅读笔记」已升级为**社区**（导航名「社区」，路由 `/community`，旧地址 `/recommend` 继续可用）。

- **帖子是说说式的**：没有标题，正文支持换行；`comic_id` 选填——留空就是纯说说，填了就是原来的「推荐作品」，卡片上带作品条并保留回详情页的入口。
- **图片只认外链**：不支持上传，服务端不接收、不落盘、不代理任何图片文件，只保存 http/https 链接（每条帖子最多 9 张，去重）。站内图片代理会按宽度重编码，对社区外链反而增加开销，所以帖子图片直接用原始链接渲染。
- **互动**：点赞、评论。点赞 / 评论 / 发帖都需要登录（影子账号），删帖与删评论由作者本人或管理员（`JM_AURA_RECOMMEND_ADMIN`）执行，能否删除由服务端在响应里算好。
- **性能约定**：点赞数、评论数、我是否点赞、评论作者昵称、可否删除全部在服务端一次算好；前端不做聚合、不做轮询，点赞只做一次乐观更新并由返回值校正，评论区**展开时才请求**并分页加载。
- **数据兼容**：点赞 / 评论 / 图片都写在 `recommendations.json` 里（schema 超集），旧帖没有这些字段时按空处理，因此历史数据**不需要任何迁移**。

## 🏗️ 项目结构

```
JM-Aura/
├── server/                     # Go 后端
│   ├── main.go                 # 入口：读取环境变量，启动 HTTP 服务
│   ├── go.mod / go.sum
│   └── internal/
│       ├── app/                # HTTP 路由、处理器、中间件
│       │   ├── router.go       # 路由注册 + SPA 嵌入 (go:embed)
│       │   ├── common.go       # 状态码、gzip、限流、op.yml 解析
│       │   ├── content.go      # 漫画内容相关处理器
│       │   ├── v2.go           # v2 多源 API 实现
│       │   ├── dl.go           # 下载任务管理器 + 图片反 scrambling
│       │   ├── site.go         # 影子账号登录/注册
│       │   ├── aura.go         # Aura 历史/备注
│       │   ├── community.go    # 社区：点赞 / 评论处理器
│       │   ├── account.go      # JM 账号管理
│       │   ├── proxy.go        # 图片代理
│       │   ├── register.go     # JM 注册
│       │   ├── adapt.go        # 适配层
│       │   └── webdist/        # 前端构建产物（embed 目标）
│       ├── db/                 # GORM 数据库层
│       ├── jm/                 # JM API 客户端（域名容错、AES解密、图片URL）
│       └── store/              # JSON 文件存储层
│           ├── store.go        # 数据目录解析 + JSON 读写
│           ├── siteauth.go     # 影子账号（PBKDF2、会话）
│           ├── credentials.go  # JM 凭据（多账号）
│           ├── cookies.go      # Cookie 持久化
│           ├── jmstore.go      # JM 状态缓存
│           ├── auralib.go      # Aura 历史/备注
│           ├── recommend.go    # 社区帖子（说说 + 可选关联作品）
│           ├── community.go    # 社区互动：图片链接、点赞、评论
│           └── profiles.go     # 设置页资料
├── frontend-react/             # React 前端
│   ├── package.json
│   ├── vite.config.ts          # 构建产物 → server/internal/app/webdist
│   └── src/
│       ├── App.tsx             # 路由 + MD3 响应式导航
│       ├── api.ts              # API 客户端（envelope 处理）
│       ├── pages/              # 13 个页面组件
│       └── ...                 # 主题、鉴权、工具函数
├── Dockerfile                  # 三阶段构建
└── README.md
```

## 🔌 API 概览

后端接口统一使用 `{st, msg, data}` 信封格式（`st=1001` 表示成功）：

- **站点/辅助 API** (`/api/*`)：影子账号登录、公告、赞助、图片代理、阅读历史等。
- **v2 多源 API** (`/api/v2/{source}/*`)：内容读取与下载的统一入口，抽象出 source 维度（当前支持 `jm`、`bika`）。

<details>
<summary>主要端点一览</summary>

| 方法   | 路径                                   | 功能                     |
| ------ | -------------------------------------- | ------------------------ |
| POST   | `/api/site/login`                      | 影子账号登录             |
| POST   | `/api/site/logout`                     | 影子账号登出             |
| GET    | `/api/site/me`                         | 获取当前用户             |
| GET    | `/api/favorites`                       | 收藏列表                 |
| GET    | `/api/image-proxy`                     | 图片代理（按需缩略）     |
| GET    | `/api/chapter_image/{photo_id}/{image_name}` | 章节图（不带 `scramble` 时原样转发，带 `scramble` 时服务端反置乱） |
| GET    | `/api/v2/{source}/comic/{comic_id}`    | v2 漫画详情（含 `is_favorite`） |
| POST   | `/api/v2/{source}/comic/{comic_id}/favorite` | v2 收藏切换（幂等，可传 `desired_state`） |
| POST   | `/api/v2/{source}/auth/login`          | v2 登录                  |
| POST   | `/api/v2/{source}/download/tasks`      | 创建下载任务（`lossless` 仅赞助者可用） |
| GET    | `/api/afdian/sponsors`                 | 爱发电赞助者名单         |
| GET    | `/api/afdian/binding`                  | 查询当前账号赞助绑定状态 |
| POST   | `/api/afdian/binding`                  | 校验订单号并绑定（解锁无损下载） |
| GET    | `/api/recommend`                       | 社区帖子列表（说说式，按时间倒序分页；含点赞数/评论数/我是否点赞/可否删除） |
| POST   | `/api/recommend`                       | 发帖（`comic_id` 选填，填了即「推荐作品」；`images` 为外链图片，最多 9 张） |
| DELETE | `/api/recommend/{id}`                  | 删帖（作者本人或管理员） |
| POST   | `/api/recommend/{id}/like`             | 点赞 / 取消点赞（登录用户，返回 `liked` + `like_count`） |
| GET    | `/api/recommend/{id}/comments`         | 评论列表（公开，倒序分页） |
| POST   | `/api/recommend/{id}/comments`         | 发表评论（登录用户）     |
| DELETE | `/api/recommend/comments/{id}`         | 删除评论（作者本人或管理员） |
| ...    | ...                                    | 完整列表见 router.go     |

</details>

## 🛠️ 常见问题

**Q: 页面能打开，但图片不显示/加载慢？**
先多刷新几次，确保服务器能正常访问外网。JM Client 内置多域名容错，会自动 failover；必要时可重启服务触发域名刷新。

**Q: 评论发不出去？**
上游有风控，请避免过短/重复内容，稍等再发。

**Q: 下载面板里的「无损模式」是灰的、或提示「赞助专享」？**
无损 PNG 为爱发电赞助者专享。在「设置」→「爱发电赞助」填入爱发电订单号（爱发电「我的」→「订单」可复制）并点「验证并绑定」，通过后立即解锁。一个订单号只能绑定一个账号，绑定一次长期有效；未配置 `JM_AURA_AFDIAN_USER_ID` / `JM_AURA_AFDIAN_TOKEN` 时该功能不可用。为避免有人反复提交把校验放大成对爱发电的批量查询，同一账号的绑定请求设有 20 秒冷却。

**Q: 如何更新？**
Docker 部署：重新 `docker build` 并重启容器即可，数据卷不受影响。
源码部署：拉取最新代码，重新构建前端 + 后端，替换二进制后重启。

**Q: 不配数据库能用吗？**
可以。不设 `DATABASE_URL` 时自动使用纯 Go SQLite（`jmaura.db`），零依赖零配置。数据库仅用于持久化下载任务记录，其余运行态数据（Cookie、凭据、影子账号、Aura 历史等）保存在 JSON 文件中。

**Q: 下载的 ZIP 在哪里？**
下载产物默认在数据目录下的 `downloads/` 子目录，临时图片缓存在 `downloads/tasks/`。可在设置页执行缓存清理。

**Q: 支持多用户吗？**
支持。每个浏览器首次访问会自动获得一个游客 ID（`jm_aura_gid`）；登录后升级为影子账号（`jm_aura_sid`，7 天有效），各用户数据相互隔离。

## ⚠️ 免责声明

本项目仅供学习交流使用。使用者应遵守当地法律法规及目标网站使用条款；开发者不对使用本项目产生的任何后果负责。
