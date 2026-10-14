import { lazy, Suspense, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { Link as RouterLink, NavLink, Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import AppBar from '@mui/material/AppBar'
import Avatar from '@mui/material/Avatar'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import ButtonBase from '@mui/material/ButtonBase'
import Chip from '@mui/material/Chip'
import Divider from '@mui/material/Divider'
import Drawer from '@mui/material/Drawer'
import IconButton from '@mui/material/IconButton'
import List from '@mui/material/List'
import ListItemButton from '@mui/material/ListItemButton'
import ListItemIcon from '@mui/material/ListItemIcon'
import ListItemText from '@mui/material/ListItemText'
import Menu from '@mui/material/Menu'
import MenuItem from '@mui/material/MenuItem'
import Paper from '@mui/material/Paper'
import Stack from '@mui/material/Stack'
import Toolbar from '@mui/material/Toolbar'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import useMediaQuery from '@mui/material/useMediaQuery'
import { useTheme } from '@mui/material/styles'
import ArrowDropDownIcon from '@mui/icons-material/ArrowDropDown'
import DarkModeIcon from '@mui/icons-material/DarkMode'
import DownloadIcon from '@mui/icons-material/Download'
import ExploreIcon from '@mui/icons-material/Explore'
import FavoriteIcon from '@mui/icons-material/Favorite'
import HistoryIcon from '@mui/icons-material/History'
import HomeIcon from '@mui/icons-material/Home'
import InfoOutlinedIcon from '@mui/icons-material/InfoOutlined'
import LightModeIcon from '@mui/icons-material/LightMode'
import LogoutIcon from '@mui/icons-material/Logout'
import LoginIcon from '@mui/icons-material/Login'
import MenuBookIcon from '@mui/icons-material/MenuBook'
import MenuIcon from '@mui/icons-material/Menu'
import MoreHorizIcon from '@mui/icons-material/MoreHoriz'
import SearchIcon from '@mui/icons-material/Search'
import SettingsIcon from '@mui/icons-material/Settings'
import ShuffleIcon from '@mui/icons-material/Shuffle'
import StarIcon from '@mui/icons-material/Star'
import SwipeVerticalIcon from '@mui/icons-material/SwipeVertical'
import ForumOutlinedIcon from '@mui/icons-material/ForumOutlined'
import VolunteerActivismIcon from '@mui/icons-material/VolunteerActivism'
import WhatshotIcon from '@mui/icons-material/Whatshot'
import { useAuth } from './auth'
import { useThemeMode } from './mode'
import {
  appLink,
  bikaSupportsLogical,
  logicalPathOf,
  sourceFromPath,
  sourcePath,
  sourcePrefix,
  SOURCES,
  type SourceKind,
} from './source'
import { AFDIAN_URL, BRAND_GRADIENT, HEADING_FONT } from './theme'
import { CenterLoading, OnlineBadge } from './components'
import Announcement from './components/Announcement'
import { BikaBindPrompt } from './components/BikaBindPrompt'

const Home = lazy(() => import('./pages/Home'))
const Search = lazy(() => import('./pages/Search'))
const Categories = lazy(() => import('./pages/Categories'))
const Leaderboard = lazy(() => import('./pages/Leaderboard'))
const Daily = lazy(() => import('./pages/Daily'))
const Latest = lazy(() => import('./pages/Latest'))
const RandomPage = lazy(() => import('./pages/RandomPage'))
const Community = lazy(() => import('./pages/Community'))
const ComicDetail = lazy(() => import('./pages/ComicDetail'))
const Reader = lazy(() => import('./pages/Reader'))
const Favorites = lazy(() => import('./pages/Favorites'))
const AuraHistory = lazy(() => import('./pages/AuraHistory'))
const Downloads = lazy(() => import('./pages/Downloads'))
const Settings = lazy(() => import('./pages/Settings'))
const Login = lazy(() => import('./pages/Login'))
const NovelList = lazy(() => import('./pages/NovelList'))
const NovelDetail = lazy(() => import('./pages/NovelDetail'))
const NovelReader = lazy(() => import('./pages/NovelReader'))
const About = lazy(() => import('./pages/About'))

// 源页面表：同一份定义渲染到 JM（无前缀 + /jm）与哔咔（/bika）两套前缀下，
// 避免重复维护路由；哔咔不支持的能力（小说、legacy 最新）不挂载。
const JM_PAGES: Array<[string, ReactNode]> = [
  ['search', <Search />],
  ['categories', <Categories />],
  ['leaderboard', <Leaderboard />],
  ['latest', <Latest />],
  ['random', <RandomPage />],
  ['community', <Community />],
  // 旧链接「阅读笔记」继续可用，指向同一个社区板块。
  ['recommend', <Community />],
  ['comic/:comicId', <ComicDetail />],
  ['reader/:chapterId', <Reader />],
  ['novels', <NovelList />],
  ['novel/:novelId', <NovelDetail />],
  ['novel_reader/:chapterId', <NovelReader />],
  ['favorites', <Favorites />],
  ['history', <AuraHistory />],
  ['downloads', <Downloads />],
  ['settings', <Settings />],
  ['about', <About />],
  ['login', <Login />],
]

const BIKA_PAGES: Array<[string, ReactNode]> = [
  ['search', <Search />],
  ['categories', <Categories />],
  ['random', <RandomPage />],
  ['comic/:comicId', <ComicDetail />],
  ['reader/:chapterId', <Reader />],
  ['favorites', <Favorites />],
  ['history', <AuraHistory />],
  ['downloads', <Downloads />],
  ['settings', <Settings />],
  ['about', <About />],
  ['login', <Login />],
]

// Material Design 3 自适应导航：
// compact(<md) 底部 Navigation Bar / medium(md–lg) Navigation Rail /
// expanded(≥lg) Navigation Drawer；导航区容器 = surface container，
// 激活态 = secondaryContainer 胶囊 indicator。
const DRAWER_WIDTH = 260
// iOS 独立窗口（index.html 里 apple-mobile-web-app-status-bar-style=black-translucent +
// viewport-fit=cover）会把系统状态栏浮在页面最上方。顶栏若不让出顶部安全区，状态栏就会压住它，
// 表现为「顶部账号名/头像那一行看不全、也点不到」。普通浏览器与 Android 上 env() 恒为 0，
// 布局与改动前完全一致，因此这是纯增益的兼容性修复。
const TOP_INSET = 'env(safe-area-inset-top)'
// Material 默认 Toolbar 高 56px（xs）/ 64px（sm+），theme.ts 未覆盖它；
// 顶栏实际总高 = Toolbar 高 + 顶部安全区。占位块与侧栏偏移必须用同一个值，否则内容会被顶栏压住。
const APPBAR_H_SM = `calc(64px + ${TOP_INSET})`
const APPBAR_HEIGHT = { xs: `calc(56px + ${TOP_INSET})`, sm: APPBAR_H_SM }
const RAIL_WIDTH = 88

interface NavItem {
  to: string
  label: string
  icon: ReactNode
}

const NAV_BROWSE: NavItem[] = [
  { to: '/', label: '首页', icon: <HomeIcon /> },
  { to: '/search', label: '搜索', icon: <SearchIcon /> },
  { to: '/novels', label: '小说', icon: <MenuBookIcon /> },
  { to: '/categories', label: '分类', icon: <ExploreIcon /> },
  { to: '/leaderboard', label: '排行榜', icon: <WhatshotIcon /> },
  { to: '/latest', label: '最新', icon: <SwipeVerticalIcon /> },
  { to: '/random', label: '随机', icon: <ShuffleIcon /> },
  { to: '/community', label: '社区', icon: <ForumOutlinedIcon /> },
]

const NAV_MINE: NavItem[] = [
  { to: '/favorites', label: '收藏夹', icon: <FavoriteIcon /> },
  { to: '/history', label: '阅读历史', icon: <HistoryIcon /> },
  { to: '/downloads', label: '下载管理', icon: <DownloadIcon /> },
  { to: '/settings', label: '设置', icon: <SettingsIcon /> },
  { to: '/about', label: '关于', icon: <InfoOutlinedIcon /> },
]

// bika 源没有小说，也没有 JM 专有的 legacy「最新」流；社区板块同样只在 JM 下挂载。
const NAV_SOURCE_HIDDEN: Record<SourceKind, Set<string>> = {
  jm: new Set<string>(),
  bika: new Set<string>(['/novels', '/latest', '/community']),
}

function navBrowseFor(kind: SourceKind): NavItem[] {
  const hidden = NAV_SOURCE_HIDDEN[kind]
  return NAV_BROWSE.filter((i) => !hidden.has(i.to))
}

// 底部栏 / 导航栏最多 5 个目的地（M3 规范），其余入口收进「更多」抽屉
function navPrimaryFor(kind: SourceKind): NavItem[] {
  const browse = navBrowseFor(kind)
  return [browse[0], browse[1], browse[2], NAV_MINE[0]]
}

function isActive(to: string, pathname: string): boolean {
  const prefix = sourcePrefix(pathname)
  if (to === '/') return pathname === prefix || pathname === prefix + '/'
  return pathname === prefix + to || pathname.startsWith(prefix + to + '/')
}

// 顶栏词标：JM Aura / Bika Aura；标签页标题沿用 JM-Aura / BikaAura。
const SOURCE_BRAND: Record<SourceKind, { wordmark: string; title: string }> = {
  jm: { wordmark: 'JM Aura', title: 'JM-Aura' },
  bika: { wordmark: 'Bika Aura', title: 'BikaAura' },
}

/** 标题即源切换器：点击标题里的品牌名弹出源选择框（原独立切换按钮已并入这里）。 */
function Logo() {
  const location = useLocation()
  const navigate = useNavigate()
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null)
  const current = sourceFromPath(location.pathname)
  const brand = SOURCE_BRAND[current]

  // 把当前页面映射到另一个源的等价路径（不支持则落到该源首页）。
  const switchTo = (kind: SourceKind) => {
    setAnchorEl(null)
    if (kind === current) return
    const logical = logicalPathOf(location.pathname)
    const target = kind === 'bika' && !bikaSupportsLogical(logical) ? '/' : logical
    navigate(sourcePath(kind, target))
  }

  return (
    <>
      <Tooltip title="切换内容源">
        <Typography
          variant="h6"
          component="button"
          type="button"
          onClick={(e) => setAnchorEl(e.currentTarget)}
          aria-label="切换内容源"
          aria-haspopup="menu"
          aria-expanded={!!anchorEl}
          sx={{
            display: 'inline-flex',
            alignItems: 'center',
            gap: 0.5,
            p: 0,
            border: 0,
            bgcolor: 'transparent',
            color: 'inherit',
            fontFamily: HEADING_FONT,
            textTransform: 'none',
            cursor: 'pointer',
            flexShrink: 0,
            fontWeight: 700,
            letterSpacing: '-0.02em',
            '& span': {
              background: BRAND_GRADIENT,
              WebkitBackgroundClip: 'text',
              backgroundClip: 'text',
              color: 'transparent',
            },
          }}
        >
          <span>{brand.wordmark}</span>
          {/* 倒三角：提示「品牌名可点击切换内容源」；展开时翻转 180° 作反馈。 */}
          <ArrowDropDownIcon
            sx={{
              fontSize: 22,
              color: 'text.secondary',
              transition: 'transform .2s ease',
              transform: anchorEl ? 'rotate(180deg)' : 'none',
            }}
          />
        </Typography>
      </Tooltip>
      <Menu anchorEl={anchorEl} open={!!anchorEl} onClose={() => setAnchorEl(null)}>
        {SOURCES.map((s) => (
          <MenuItem key={s.kind} selected={s.kind === current} onClick={() => switchTo(s.kind)}>
            {s.label}
          </MenuItem>
        ))}
      </Menu>
    </>
  )
}

function SectionLabel({ children }: { children: ReactNode }) {
  return (
    <Typography
      sx={{
        px: 3,
        pt: 2,
        pb: 0.5,
        fontSize: 12.5,
        fontWeight: 600,
        letterSpacing: '0.05em',
        color: 'text.secondary',
      }}
    >
      {children}
    </Typography>
  )
}

/** Navigation Bar / Rail 共用的胶囊目的地按钮 */
function NavPill({
  item,
  active,
  onClick,
}: {
  item: NavItem
  active: boolean
  onClick: () => void
}) {
  return (
    <ButtonBase
      onClick={onClick}
      aria-label={item.label}
      aria-current={active ? 'page' : undefined}
      sx={{
        flexDirection: 'column',
        gap: 0.5,
        py: 0.75,
        px: 0.5,
        minWidth: 60,
        borderRadius: 2.5,
        WebkitTapHighlightColor: 'transparent',
      }}
    >
      <Box
        sx={{
          width: 58,
          height: 32,
          borderRadius: '999px',
          display: 'grid',
          placeItems: 'center',
          bgcolor: active ? 'aura.secondaryContainer' : 'transparent',
          transition: 'background-color .2s ease',
          '& .MuiSvgIcon-root': {
            fontSize: 22,
            color: active ? 'aura.onSecondaryContainer' : 'aura.onSurfaceVariant',
          },
        }}
      >
        {item.icon}
      </Box>
      <Typography
        sx={{
          fontSize: 12,
          lineHeight: 1,
          fontWeight: active ? 600 : 500,
          color: active ? 'text.primary' : 'text.secondary',
        }}
      >
        {item.label}
      </Typography>
    </ButtonBase>
  )
}

function NavListItems({ items, onNavigate }: { items: NavItem[]; onNavigate?: () => void }) {
  const location = useLocation()
  return (
    <List disablePadding>
      {items.map((item) => (
        <ListItemButton
          key={item.to}
          component={NavLink}
          to={appLink(item.to)}
          end={item.to === '/'}
          onClick={onNavigate}
          selected={isActive(item.to, location.pathname)}
        >
          <ListItemIcon>{item.icon}</ListItemIcon>
          <ListItemText primary={item.label} primaryTypographyProps={{ fontSize: 14.5 }} />
        </ListItemButton>
      ))}
    </List>
  )
}

/** expanded 抽屉内容：浏览 / 我的 两个分区 */
function DrawerContent({ onNavigate }: { onNavigate?: () => void }) {
  const kind = sourceFromPath()
  return (
    <Box sx={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <Toolbar sx={{ px: 2.5 }}>
        <Logo />
      </Toolbar>
      <Box sx={{ flexGrow: 1, overflowY: 'auto', pb: 2 }}>
        <SectionLabel>浏览</SectionLabel>
        <NavListItems items={navBrowseFor(kind)} onNavigate={onNavigate} />
        <Divider sx={{ mx: 3, mt: 1.5, opacity: 0.7 }} />
        <SectionLabel>我的</SectionLabel>
        <NavListItems items={NAV_MINE} onNavigate={onNavigate} />
      </Box>
    </Box>
  )
}

/** medium 断点：Navigation Rail */
function RailContent({ onMore }: { onMore: () => void }) {
  const location = useLocation()
  const navigate = useNavigate()
  const kind = sourceFromPath()
  return (
    <Box
      sx={{
        height: '100%',
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        py: 1.5,
        gap: 0.5,
      }}
    >
      <IconButton
        onClick={onMore}
        aria-label="打开全部导航"
        sx={{ mb: 1, color: 'aura.onSurfaceVariant' }}
      >
        <MenuIcon />
      </IconButton>
      {navPrimaryFor(kind).map((item) => (
        <NavPill
          key={item.to}
          item={item}
          active={isActive(item.to, location.pathname)}
          onClick={() => navigate(appLink(item.to))}
        />
      ))}
      <Box sx={{ flexGrow: 1 }} />
      <NavPill item={{ to: '#', label: '更多', icon: <MoreHorizIcon /> }} active={false} onClick={onMore} />
    </Box>
  )
}

/** compact 断点：底部 Navigation Bar */
function BottomBar({ onMore }: { onMore: () => void }) {
  const location = useLocation()
  const navigate = useNavigate()
  const kind = sourceFromPath()
  return (
    <Paper
      square
      elevation={0}
      sx={{
        position: 'fixed',
        left: 0,
        right: 0,
        bottom: 0,
        zIndex: (t) => t.zIndex.appBar,
        bgcolor: 'aura.surfaceContainer',
        borderTop: '1px solid',
        borderColor: 'divider',
        pb: 'env(safe-area-inset-bottom)',
        display: { xs: 'block', md: 'none' },
      }}
    >
      <Box sx={{ display: 'flex', justifyContent: 'space-around', alignItems: 'flex-start', pt: 0.5 }}>
        {navPrimaryFor(kind).map((item) => (
          <NavPill
            key={item.to}
            item={item}
            active={isActive(item.to, location.pathname)}
            onClick={() => navigate(appLink(item.to))}
          />
        ))}
        <NavPill item={{ to: '#', label: '更多', icon: <MoreHorizIcon /> }} active={false} onClick={onMore} />
      </Box>
    </Paper>
  )
}

function UserArea() {
  const { user, loading, logout } = useAuth()
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null)

  if (loading) {
    return <Chip size="small" label="…" sx={{ opacity: 0.5 }} />
  }
  if (!user) {
    return (
      <Button component={RouterLink} to="/login" size="small" variant="contained" startIcon={<LoginIcon />}>
        登录
      </Button>
    )
  }
  return (
    <>
      <Tooltip title={user.username}>
        <Chip
          clickable
          avatar={
            <Avatar sx={{ bgcolor: 'primary.main', color: 'primary.contrastText', fontSize: 13 }}>
              {user.username.slice(0, 1).toUpperCase()}
            </Avatar>
          }
          label={user.username}
          onClick={(e) => setAnchorEl(e.currentTarget)}
          sx={{ maxWidth: 160 }}
        />
      </Tooltip>
      <Menu anchorEl={anchorEl} open={!!anchorEl} onClose={() => setAnchorEl(null)}>
        <MenuItem
          onClick={() => {
            setAnchorEl(null)
            void logout()
          }}
        >
          <ListItemIcon>
            <LogoutIcon fontSize="small" />
          </ListItemIcon>
          退出登录
        </MenuItem>
      </Menu>
    </>
  )
}

export default function App() {
  const theme = useTheme()
  const isRail = useMediaQuery(theme.breakpoints.up('md'))
  const isExpanded = useMediaQuery(theme.breakpoints.up('lg'))
  const [drawerOpen, setDrawerOpen] = useState(false)
  const { mode, toggle } = useThemeMode()
  const location = useLocation()
  const logicalPath = logicalPathOf(location.pathname)
  const isReader = logicalPath.startsWith('/reader/') || logicalPath.startsWith('/novel_reader/')

  // 标签页标题跟随内容源：JM-Aura / BikaAura。
  useEffect(() => {
    const brand = SOURCE_BRAND[sourceFromPath(location.pathname)]
    document.title = brand.title
  }, [location.pathname])

  const drawerPaper = useMemo(
    () => ({ width: DRAWER_WIDTH, bgcolor: 'aura.surfaceContainerLow' }),
    [],
  )

  return (
    <Box sx={{ display: 'flex', minHeight: '100dvh' }}>
      <Announcement />
      {!isReader && (
        <AppBar position="fixed" sx={{ zIndex: (t) => t.zIndex.drawer + 1, pt: TOP_INSET }}>
          <Toolbar sx={{ gap: 1 }}>
            <Logo />
            <Box sx={{ flexGrow: 1 }} />
            <Tooltip title="如果觉得好用的话，可以捐助支持作者喵呜？谢谢喵！">
              <Button
                size="small"
                color="inherit"
                href={AFDIAN_URL}
                target="_blank"
                rel="noreferrer"
                aria-label="捐助支持作者"
                sx={{
                  flexShrink: 0,
                  minWidth: 0,
                  px: { xs: 1, sm: 1.5 },
                  gap: 0.5,
                  borderRadius: 999,
                  fontWeight: 700,
                  fontSize: 13,
                  whiteSpace: 'nowrap',
                }}
              >
                <VolunteerActivismIcon sx={{ fontSize: 18 }} />
                <Box component="span" sx={{ display: { xs: 'none', sm: 'inline' } }}>
                  捐助喵
                </Box>
              </Button>
            </Tooltip>
            <Tooltip title={mode === 'dark' ? '切换到浅色' : '切换到深色'}>
              <IconButton onClick={toggle} aria-label="切换主题">
                {mode === 'dark' ? <LightModeIcon /> : <DarkModeIcon />}
              </IconButton>
            </Tooltip>
            <UserArea />
          </Toolbar>
        </AppBar>
      )}

      {isExpanded && !isReader && (
        <Drawer
          variant="permanent"
          sx={{
            width: DRAWER_WIDTH,
            flexShrink: 0,
            '& .MuiDrawer-paper': { ...drawerPaper, position: 'relative', mt: APPBAR_H_SM },
          }}
        >
          <DrawerContent />
        </Drawer>
      )}

      {!isExpanded && isRail && !isReader && (
        <Drawer
          variant="permanent"
          sx={{
            width: RAIL_WIDTH,
            flexShrink: 0,
            '& .MuiDrawer-paper': {
              width: RAIL_WIDTH,
              position: 'relative',
              mt: APPBAR_H_SM,
              bgcolor: 'aura.surfaceContainer',
              boxSizing: 'border-box',
            },
          }}
        >
          <RailContent onMore={() => setDrawerOpen(true)} />
        </Drawer>
      )}

      {!isRail && !isReader && <BottomBar onMore={() => setDrawerOpen(true)} />}

      {!isExpanded && !isReader && (
        <Drawer open={drawerOpen} onClose={() => setDrawerOpen(false)} slotProps={{ paper: { sx: drawerPaper } }}>
          <DrawerContent onNavigate={() => setDrawerOpen(false)} />
        </Drawer>
      )}

      <Box
        component="main"
        sx={{
          flexGrow: 1,
          minWidth: 0,
          p: { xs: 2, sm: 3 },
          pb: { xs: 'calc(92px + env(safe-area-inset-bottom))', md: 3 },
          maxWidth: 1400,
          mx: 'auto',
          ...(isReader && { p: 0, maxWidth: 'none' }),
        }}
      >
        {!isReader && <Box sx={{ height: APPBAR_HEIGHT }} />}
        <Suspense fallback={<CenterLoading />}>
          <Routes>
            {/* JM：无前缀（历史兼容形态）与 /jm 别名并存 */}
            <Route path="/" element={<Home />} />
            {JM_PAGES.map(([p, el]) => (
              <Route key={p} path={'/' + p} element={el} />
            ))}
            <Route path="/jm" element={<Home />} />
            {JM_PAGES.map(([p, el]) => (
              <Route key={'jm-' + p} path={'/jm/' + p} element={el} />
            ))}

            {/* 哔咔：/bika 前缀，仅挂载其支持的能力 */}
            <Route path="/bika" element={<Daily />} />
            {BIKA_PAGES.map(([p, el]) => (
              <Route key={'bika-' + p} path={'/bika/' + p} element={el} />
            ))}

            <Route path="*" element={<Navigate to={appLink('/')} replace />} />
          </Routes>
        </Suspense>
        {!isReader && (
          <Box component="footer" sx={{ py: 5, textAlign: 'center' }}>
            <Stack direction="row" spacing={1} justifyContent="center" alignItems="center">
              <StarIcon fontSize="small" sx={{ opacity: 0.4 }} />
              <Typography variant="caption" color="text.secondary">
                由 Material Design 3 与 Go 驱动
              </Typography>
            </Stack>
            <Box sx={{ mt: 1 }}>
              <OnlineBadge />
            </Box>
            <Typography
              component={RouterLink}
              to="/about"
              variant="caption"
              sx={{
                display: 'inline-block',
                mt: 1,
                color: 'text.secondary',
                textDecoration: 'none',
                '&:hover': { color: 'primary.main' },
              }}
            >
              关于 JM-Aura
            </Typography>
          </Box>
        )}
      </Box>

      {/* 首次切换到哔咔且未绑定时，弹一次绑定引导（游客不弹） */}
      <BikaBindPrompt />
    </Box>
  )
}
