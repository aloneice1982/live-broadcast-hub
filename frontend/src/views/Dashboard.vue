<script setup lang="ts">
/**
 * Dashboard.vue — 超管全省大盘
 */
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import {
  citiesAPI, streamSourcesAPI, usersAPI, systemAPI, auditAPI,
  type City, type ProcessStatus, type StreamSource, type User, type AuditLog
} from '@/api'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const auth = useAuthStore()
const isObserver = computed(() => auth.user?.role === 'observer')
const activeTab = ref<'overview' | 'sources' | 'users' | 'logs'>('overview')

// ── 地市状态 ──────────────────────────────────────────────────
const cities = ref<City[]>([])
const statuses = ref<Record<number, ProcessStatus>>({})
let pollingTimer: ReturnType<typeof setInterval>
let tickTimer: ReturnType<typeof setInterval>
const tick = ref(0)  // 每秒递增，驱动计时器重算

async function loadCities() {
  const res = await citiesAPI.list()
  cities.value = res.data.data ?? []
  fetchAllStatuses()
}

async function fetchAllStatuses() {
  await Promise.allSettled(
    cities.value.map(async city => {
      try {
        const res = await citiesAPI.getStatus(city.id)
        statuses.value[city.id] = res.data.data
      } catch { /* 忽略 */ }
    })
  )
}

async function resetCityFFmpeg(cityId: number, event: Event) {
  event.stopPropagation()
  try {
    await citiesAPI.resetFFmpeg(cityId)
    const res = await citiesAPI.getStatus(cityId)
    statuses.value[cityId] = res.data.data
  } catch { /* 静默 */ }
}

const statusLabel: Record<string, string> = {
  idle: '空闲', warming: '预热', streaming: '推流中',
  failed: '异常', breaker_open: '熔断！', waiting: '等待启动'
}
const statusDot: Record<string, string> = {
  idle: 'bg-gray-500',
  warming: 'bg-yellow-400 animate-pulse',
  streaming: 'bg-green-400',
  failed: 'bg-red-400 animate-pulse',
  breaker_open: 'bg-red-500 animate-ping',
  waiting: 'bg-blue-400 animate-pulse'
}
const statusBorder: Record<string, string> = {
  idle: 'border-gray-700',
  warming: 'border-yellow-700',
  streaming: 'border-green-700',
  failed: 'border-red-700',
  breaker_open: 'border-red-500',
  waiting: 'border-blue-700'
}

function displayStatus(cityId: number): string {
  const s = statuses.value[cityId]
  if (!s) return 'idle'
  if (s.status !== 'idle') return s.status
  if (s.schedulerActive) return 'waiting'
  return 'idle'
}

// 推流时长计时器（基于 lastStartedAt 东八区时间字符串）
function formatDuration(startedAt: string | undefined): string {
  tick.value  // reactive dependency
  if (!startedAt) return ''
  // 规范化时间字符串：剥除所有时区后缀（含 go-sqlite3 误加的 Z），统一以 +08:00 解析
  const bare = startedAt.replace('Z', '').replace(/[+-]\d{2}:\d{2}$/, '')
  const normalized = bare.includes('T') ? bare : bare.replace(' ', 'T')
  const startMs = new Date(normalized + '+08:00').getTime()
  if (isNaN(startMs)) return '00:00:00'
  const secs = Math.max(0, Math.floor((Date.now() - startMs) / 1000))
  const h = Math.floor(secs / 3600)
  const m = Math.floor((secs % 3600) / 60)
  const s = secs % 60
  return `${String(h).padStart(2,'0')}:${String(m).padStart(2,'0')}:${String(s).padStart(2,'0')}`
}

// ── 带宽监控 ──────────────────────────────────────────────────
const bandwidth = ref({ uploadMbps: 0, downloadMbps: 0 })
let bandwidthTimer: ReturnType<typeof setInterval>

async function fetchBandwidth() {
  try {
    const res = await systemAPI.metrics()
    bandwidth.value = res.data.data
  } catch { /* 静默，宿主机不支持时不报错 */ }
}

// ── 直播源管理 ────────────────────────────────────────────────
const sources = ref<StreamSource[]>([])
const savingSource = ref(false)

// CSV 导入
const importFile = ref<File | null>(null)
const importing = ref(false)
const importMsg = ref('')

// 日期过滤（UTC+8 安全）
function localDateString(d = new Date()): string {
  const y = d.getFullYear()
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${y}-${m}-${day}`
}

const filterDate = ref(localDateString())
const realToday = localDateString()

const filteredSources = computed(() => {
  return sources.value
    .filter(s =>
      !s.matchDatetime ||
      s.matchDatetime.startsWith(filterDate.value) ||
      s.matchDatetime < realToday
    )
    .sort((a, b) => {
      const aExpired = a.matchDatetime && a.matchDatetime < realToday ? 1 : 0
      const bExpired = b.matchDatetime && b.matchDatetime < realToday ? 1 : 0
      return aExpired - bExpired
    })
})

// 新建直播源表单（规范化 7 字段）
const newSource = ref({
  name: '', url: '',
  matchDate: '', matchTime: '',
  round: '', channel: '', remark: ''
})

async function loadSources() {
  const res = await streamSourcesAPI.list()
  sources.value = res.data.data ?? []
}

async function addSource() {
  if (!newSource.value.name || !newSource.value.url) return
  savingSource.value = true
  try {
    const matchDatetime = newSource.value.matchDate
      ? `${newSource.value.matchDate} ${newSource.value.matchTime || '00:00'}:00`
      : undefined
    await streamSourcesAPI.create({
      name: newSource.value.name,
      url: newSource.value.url,
      matchDatetime,
      round: newSource.value.round || undefined,
      channel: newSource.value.channel || undefined,
      remark: newSource.value.remark || undefined,
    })
    newSource.value = { name: '', url: '', matchDate: '', matchTime: '', round: '', channel: '', remark: '' }
    await loadSources()
  } finally { savingSource.value = false }
}

async function downloadTemplate() {
  const res = await streamSourcesAPI.downloadTemplate()
  const url = URL.createObjectURL(res.data as Blob)
  const a = document.createElement('a')
  a.href = url; a.download = 'stream_sources_template.csv'; a.click()
  URL.revokeObjectURL(url)
}

async function handleImport() {
  if (!importFile.value) return
  importing.value = true; importMsg.value = ''
  try {
    const res = await streamSourcesAPI.importCSV(importFile.value)
    const { imported, skipped } = res.data.data
    importMsg.value = `成功导入 ${imported} 条，跳过 ${skipped} 条`
    importFile.value = null
    await loadSources()
  } catch (e: any) {
    importMsg.value = e.response?.data?.error ?? '导入失败'
  } finally { importing.value = false }
}

async function toggleSource(s: StreamSource) {
  await streamSourcesAPI.update(s.id, { ...s, isActive: !s.isActive })
  await loadSources()
}

async function deleteSource(id: number) {
  if (!confirm('确定删除该直播源？')) return
  await streamSourcesAPI.remove(id)
  await loadSources()
}

// ── 用户管理 ──────────────────────────────────────────────────
const users = ref<User[]>([])
const newUser = ref({ username: '', password: '', role: 'city_admin', cityId: undefined as number | undefined, phone: '' })
const savingUser = ref(false)
const userError = ref('')

async function loadUsers() {
  const res = await usersAPI.list()
  users.value = res.data.data ?? []
}

async function createUser() {
  userError.value = ''
  if (!newUser.value.username || !newUser.value.password) { userError.value = '用户名和密码必填'; return }
  savingUser.value = true
  try {
    await usersAPI.create(newUser.value)
    newUser.value = { username: '', password: '', role: 'city_admin', cityId: undefined, phone: '' }
    await loadUsers()
  } catch (e: any) {
    userError.value = e.response?.data?.error ?? '创建失败'
  } finally { savingUser.value = false }
}

const editingPasswordUserId = ref<number | null>(null)
const editingPassword = ref('')
const changingPassword = ref(false)
const pwError = ref('')
const showPassword = ref(false)
const pwSuccess = ref(false)

async function deleteUser(userId: number) {
  if (!confirm('确认删除该账号？此操作不可撤销')) return
  try {
    await usersAPI.remove(userId)
    await loadUsers()
  } catch (e: any) {
    userError.value = e.response?.data?.error ?? '删除失败'
  }
}

async function savePassword(userId: number) {
  pwError.value = ''
  pwSuccess.value = false
  if (editingPassword.value.length < 8) { pwError.value = '密码不能少于 8 位'; return }
  changingPassword.value = true
  try {
    await usersAPI.changePassword(userId, editingPassword.value)
    pwSuccess.value = true
    setTimeout(() => {
      editingPasswordUserId.value = null
      editingPassword.value = ''
      showPassword.value = false
      pwSuccess.value = false
    }, 1500)
  } catch (e: any) {
    pwError.value = e.response?.data?.error ?? '修改失败'
  } finally { changingPassword.value = false }
}

function logout() { auth.logout(); router.push('/login') }

// ── 操作日志 ──────────────────────────────────────────────────
const auditLogs = ref<AuditLog[]>([])
const auditTotal = ref(0)
const auditPage = ref(1)
const auditPageSize = 50
const auditFilterAction = ref('')
const auditFilterUser = ref('')
let auditTimer: ReturnType<typeof setInterval>

async function loadAuditLogs() {
  try {
    const res = await auditAPI.list({
      page: auditPage.value,
      page_size: auditPageSize,
      action: auditFilterAction.value || undefined,
      username: auditFilterUser.value || undefined,
    })
    auditLogs.value = res.data.data.logs
    auditTotal.value = res.data.data.total
  } catch { /* 静默 */ }
}

function auditSearch() {
  auditPage.value = 1
  loadAuditLogs()
}

function auditPrevPage() {
  if (auditPage.value > 1) { auditPage.value--; loadAuditLogs() }
}
function auditNextPage() {
  if (auditPage.value * auditPageSize < auditTotal.value) { auditPage.value++; loadAuditLogs() }
}

const auditActionBadge: Record<string, string> = {
  LOGIN: 'bg-green-900/60 text-green-300',
  LOGIN_FAIL: 'bg-red-900/60 text-red-300',
  START_STREAM: 'bg-blue-900/60 text-blue-300',
  STOP_STREAM: 'bg-gray-700 text-gray-300',
  CREATE_USER: 'bg-orange-900/60 text-orange-300',
  DELETE_USER: 'bg-red-900/60 text-red-300',
  CHANGE_PASSWORD: 'bg-yellow-900/60 text-yellow-300',
}
function auditBadgeClass(action: string) {
  return auditActionBadge[action] ?? 'bg-gray-700 text-gray-300'
}

onMounted(() => {
  loadCities(); loadSources(); loadUsers(); fetchBandwidth()
  if (!isObserver.value) { loadAuditLogs(); auditTimer = setInterval(loadAuditLogs, 30000) }
  pollingTimer = setInterval(fetchAllStatuses, 5000)
  tickTimer = setInterval(() => tick.value++, 1000)
  bandwidthTimer = setInterval(fetchBandwidth, 5000)
})
onUnmounted(() => {
  clearInterval(pollingTimer)
  clearInterval(tickTimer)
  clearInterval(bandwidthTimer)
  clearInterval(auditTimer)
})
</script>

<template>
  <div class="min-h-screen bg-gray-950">
    <!-- 顶栏 -->
    <header class="border-b border-gray-800 bg-gray-900/80 backdrop-blur sticky top-0 z-10">
      <div class="max-w-7xl mx-auto px-4 h-14 flex items-center justify-between">
        <div class="flex items-center gap-3">
          <span class="text-xl">📡</span>
          <h1 class="font-bold text-white">苏超联赛直播分发中台</h1>
          <span class="badge bg-blue-900/50 text-blue-300">{{ isObserver ? '观察员大盘' : '超管大盘' }}</span>
        </div>
        <button class="btn-ghost text-sm" @click="logout">退出登录</button>
      </div>
    </header>

    <main class="max-w-7xl mx-auto px-4 py-6">
      <!-- 选项卡（隐藏"排期管理"） -->
      <div class="flex gap-1 mb-6 bg-gray-900 border border-gray-800 rounded-xl p-1 w-fit">
        <button
          v-for="tab in (isObserver ? [{ key: 'overview', label: '全省大盘' }] : [{ key: 'overview', label: '全省大盘' }, { key: 'sources', label: '直播源管理' }, { key: 'users', label: '用户管理' }, { key: 'logs', label: '操作日志' }])"
          :key="tab.key"
          class="px-4 py-1.5 rounded-lg text-sm font-medium transition-colors"
          :class="activeTab === tab.key ? 'bg-blue-600 text-white' : 'text-gray-400 hover:text-gray-200'"
          @click="activeTab = tab.key as any"
        >{{ tab.label }}</button>
      </div>

      <!-- ── 全省大盘 ── -->
      <div v-if="activeTab === 'overview'">

        <!-- 带宽监控行 -->
        <div class="flex items-center gap-5 mb-4 px-1">
          <span class="text-xs text-gray-500">服务器带宽</span>
          <span class="text-xs font-mono text-green-400">↑ {{ bandwidth.uploadMbps.toFixed(1) }} Mbps</span>
          <span class="text-xs font-mono text-blue-400">↓ {{ bandwidth.downloadMbps.toFixed(1) }} Mbps</span>
        </div>

        <!-- 统计行 -->
        <div class="grid grid-cols-2 sm:grid-cols-4 gap-4 mb-6">
          <div class="card text-center">
            <p class="text-2xl font-bold text-green-400">
              {{ Object.values(statuses).filter(s => s.status === 'streaming').length }}
            </p>
            <p class="text-xs text-gray-400 mt-1">推流中</p>
          </div>
          <div class="card text-center">
            <p class="text-2xl font-bold text-yellow-400">
              {{ Object.values(statuses).filter(s => s.status === 'warming').length }}
            </p>
            <p class="text-xs text-gray-400 mt-1">预热中</p>
          </div>
          <div class="card text-center">
            <p class="text-2xl font-bold text-red-400">
              {{ Object.values(statuses).filter(s => ['failed','breaker_open'].includes(s.status)).length }}
            </p>
            <p class="text-xs text-gray-400 mt-1">异常 / 熔断</p>
          </div>
          <div class="card text-center">
            <p class="text-2xl font-bold text-gray-400">
              {{ Object.values(statuses).filter(s => s.status === 'idle').length }}
            </p>
            <p class="text-xs text-gray-400 mt-1">空闲</p>
          </div>
        </div>

        <!-- 地市卡片网格 -->
        <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5 gap-3">
          <div
            v-for="city in cities"
            :key="city.id"
            class="card border transition-all h-28 flex flex-col justify-between overflow-hidden"
            :class="[statusBorder[displayStatus(city.id)], isObserver ? 'cursor-default' : 'cursor-pointer hover:scale-[1.02]']"
            @click="!isObserver && router.push(`/city/${city.id}`)"
          >
            <!-- 顶部：城市名 + code -->
            <div class="flex items-center justify-between">
              <span class="text-sm font-semibold text-white">{{ city.name }}</span>
              <span class="text-xs text-gray-500">{{ city.code }}</span>
            </div>
            <!-- 底部：状态内容 -->
            <div>
              <!-- 推流中 / 预热中 -->
              <template v-if="['streaming','warming'].includes(statuses[city.id]?.status)">
                <p v-if="statuses[city.id]?.currentItemName"
                   class="text-xs text-gray-400 truncate"
                   :title="statuses[city.id].currentItemName">
                  {{ statuses[city.id].currentItemName }}
                </p>
                <div class="flex items-center justify-between mt-1.5">
                  <div class="flex items-center gap-1.5">
                    <span class="w-2 h-2 rounded-full shrink-0" :class="statusDot[displayStatus(city.id)]" />
                    <span class="text-xs text-gray-400">{{ statusLabel[displayStatus(city.id)] }}</span>
                  </div>
                  <span v-if="statuses[city.id]?.lastStartedAt"
                        class="text-xs font-mono text-green-400 tabular-nums">
                    {{ formatDuration(statuses[city.id].lastStartedAt) }}
                  </span>
                </div>
              </template>
              <!-- 其他状态 -->
              <template v-else>
                <div class="flex items-center gap-1.5">
                  <span class="w-2 h-2 rounded-full shrink-0" :class="statusDot[displayStatus(city.id)]" />
                  <span class="text-xs text-gray-400">{{ statusLabel[displayStatus(city.id)] }}</span>
                </div>
                <p v-if="statuses[city.id]?.retryCount" class="text-xs text-yellow-400 mt-1">
                  重试 {{ statuses[city.id].retryCount }}/3
                </p>
                <p v-else-if="displayStatus(city.id) === 'waiting' && statuses[city.id]?.scheduleStatus === 'running'"
                   class="text-xs text-blue-400 mt-1">
                  排期已启动，等待开播
                </p>
                <p v-else-if="statuses[city.id]?.status === 'idle' && statuses[city.id]?.todayItemCount"
                   class="text-xs text-gray-500 mt-1">
                  今日 {{ statuses[city.id].todayItemCount }} 段排期
                </p>
                <button
                  v-if="statuses[city.id]?.status === 'breaker_open' && !isObserver"
                  class="mt-1.5 w-full text-xs px-2 py-1 rounded border border-red-600 text-red-400 hover:bg-red-900/30 transition-colors"
                  @click.stop="resetCityFFmpeg(city.id, $event)"
                >清除熔断</button>
              </template>
            </div>
          </div>
        </div>
      </div>

      <!-- ── 直播源管理 ── -->
      <div v-if="activeTab === 'sources'" class="max-w-2xl space-y-4">
        <!-- CSV 批量导入 -->
        <div class="card space-y-3">
          <h3 class="text-sm font-semibold text-gray-300">CSV 批量导入</h3>
          <div class="flex flex-wrap gap-2 items-center">
            <button class="btn-ghost text-sm" @click="downloadTemplate">⬇ 下载模板</button>
            <label class="relative cursor-pointer">
              <span class="btn-ghost text-sm">{{ importFile ? importFile.name : '选择 CSV 文件' }}</span>
              <input type="file" accept=".csv" class="absolute inset-0 opacity-0 cursor-pointer"
                     @change="e => importFile = (e.target as HTMLInputElement).files?.[0] ?? null" />
            </label>
            <button class="btn-primary text-sm" :disabled="!importFile || importing" @click="handleImport">
              {{ importing ? '导入中…' : '导入' }}
            </button>
          </div>
          <p v-if="importMsg" class="text-xs" :class="importMsg.startsWith('成功') ? 'text-green-400' : 'text-red-400'">
            {{ importMsg }}
          </p>
        </div>

        <!-- 手动添加（规范化 7 字段） -->
        <div class="card space-y-3">
          <h3 class="text-sm font-semibold text-gray-300">手动添加直播源</h3>
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <div class="sm:col-span-2">
              <label class="label">名称 *</label>
              <input v-model="newSource.name" class="input" placeholder="苏超流A-泰州vs南京" />
            </div>
            <div>
              <label class="label">比赛日期</label>
              <input v-model="newSource.matchDate" type="date" class="input" />
            </div>
            <div>
              <label class="label">比赛时间</label>
              <input v-model="newSource.matchTime" type="time" class="input" />
            </div>
            <div>
              <label class="label">轮次（可选）</label>
              <input v-model="newSource.round" class="input" placeholder="第3轮" />
            </div>
            <div>
              <label class="label">所属频道（可选）</label>
              <input v-model="newSource.channel" class="input" placeholder="CCTV5" />
            </div>
            <div class="sm:col-span-2">
              <label class="label">备注（可选）</label>
              <input v-model="newSource.remark" class="input" placeholder="备用流" />
            </div>
            <div class="sm:col-span-2">
              <label class="label">RTMP / HLS 地址 *</label>
              <input v-model="newSource.url" class="input" placeholder="rtmp://..." />
            </div>
          </div>
          <button class="btn-primary" :disabled="savingSource || !newSource.name || !newSource.url" @click="addSource">
            + 添加
          </button>
        </div>

        <!-- 日期过滤 -->
        <div class="flex items-center gap-3 flex-wrap">
          <label class="text-xs text-gray-400 shrink-0">显示日期</label>
          <input v-model="filterDate" type="date" class="input py-1 text-sm w-auto" />
          <button
            v-if="filterDate !== localDateString()"
            class="text-xs text-gray-500 hover:text-gray-300"
            @click="filterDate = localDateString()"
          >回到今天</button>
          <span class="text-xs text-gray-600">今日源 + 常驻源置顶，已过期置灰</span>
        </div>

        <!-- 直播源列表 -->
        <div class="space-y-2">
          <div
            v-for="s in filteredSources"
            :key="s.id"
            class="card flex items-center gap-3"
            :class="s.matchDatetime && s.matchDatetime < realToday ? 'opacity-50' : ''"
          >
            <span :class="s.isActive ? 'text-green-400' : 'text-gray-600'" class="text-lg shrink-0">●</span>
            <div class="flex-1 min-w-0">
              <div class="flex items-center gap-2">
                <p class="text-sm font-medium text-white truncate">{{ s.name }}</p>
                <span v-if="s.matchDatetime && s.matchDatetime < realToday"
                      class="shrink-0 text-xs px-1.5 py-0.5 rounded bg-gray-700 text-gray-400">已过期</span>
              </div>
              <p class="text-xs text-gray-500 font-mono truncate">{{ s.url }}</p>
              <p v-if="s.matchDatetime || s.round || s.channel || s.remark"
                 class="text-xs text-gray-500 mt-0.5 space-x-2">
                <span v-if="s.matchDatetime">{{ s.matchDatetime }}</span>
                <span v-if="s.round">{{ s.round }}</span>
                <span v-if="s.channel">{{ s.channel }}</span>
                <span v-if="s.remark" class="text-gray-600">{{ s.remark }}</span>
              </p>
            </div>
            <button class="btn-ghost text-xs py-1 px-2" @click="toggleSource(s)">
              {{ s.isActive ? '下线' : '上线' }}
            </button>
            <button class="text-gray-600 hover:text-red-400 transition-colors text-lg" @click="deleteSource(s.id)">✕</button>
          </div>
          <p v-if="filteredSources.length === 0" class="text-gray-500 text-sm text-center py-4">
            {{ filterDate === localDateString() ? '今日暂无直播源' : `${filterDate} 暂无直播源` }}
          </p>
        </div>
      </div>

      <!-- ── 用户管理 ── -->
      <div v-if="activeTab === 'users'" class="max-w-2xl space-y-4">
        <div class="card space-y-3">
          <h3 class="text-sm font-semibold text-gray-300">新建账号</h3>
          <div class="grid grid-cols-2 gap-3">
            <div><label class="label">用户名</label><input v-model="newUser.username" class="input" /></div>
            <div><label class="label">密码（≥8位）</label><input v-model="newUser.password" type="password" class="input" /></div>
            <div>
              <label class="label">角色</label>
              <select v-model="newUser.role" class="input">
                <option value="city_admin">地市管理员</option>
                <option value="super_admin">超级管理员</option>
                <option value="observer">观察员（只读大盘）</option>
              </select>
            </div>
            <div v-if="newUser.role === 'city_admin'">
              <label class="label">所属城市</label>
              <select v-model="newUser.cityId" class="input">
                <option :value="undefined" disabled>— 请选择 —</option>
                <option v-for="c in cities" :key="c.id" :value="c.id">{{ c.name }}</option>
              </select>
            </div>
            <div><label class="label">手机号（告警）</label><input v-model="newUser.phone" class="input" placeholder="138xxxxxxxx" /></div>
          </div>
          <p v-if="userError" class="text-xs text-red-400">⚠ {{ userError }}</p>
          <button class="btn-primary" :disabled="savingUser" @click="createUser">创建账号</button>
        </div>

        <div class="card overflow-hidden p-0">
          <table class="w-full text-sm">
            <thead class="bg-gray-800 text-gray-400 text-xs">
              <tr>
                <th class="px-4 py-2.5 text-left">用户名</th>
                <th class="px-4 py-2.5 text-left">角色</th>
                <th class="px-4 py-2.5 text-left">城市</th>
                <th class="px-4 py-2.5 text-left">手机</th>
                <th class="px-4 py-2.5 text-right">操作</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-800">
              <template v-for="u in users" :key="u.id">
                <tr class="hover:bg-gray-800/50 transition-colors">
                  <td class="px-4 py-2.5 font-medium text-white">{{ u.username }}</td>
                  <td class="px-4 py-2.5">
                    <span :class="u.role === 'super_admin' ? 'badge bg-blue-900/50 text-blue-300' : u.role === 'observer' ? 'badge bg-purple-900/50 text-purple-300' : 'badge bg-gray-700 text-gray-300'">
                      {{ u.role === 'super_admin' ? '超管' : u.role === 'observer' ? '观察员' : '地市' }}
                    </span>
                  </td>
                  <td class="px-4 py-2.5 text-gray-400">{{ cities.find(c => c.id === u.cityId)?.name ?? '—' }}</td>
                  <td class="px-4 py-2.5 text-gray-400 font-mono text-xs">{{ u.phone ?? '—' }}</td>
                  <td class="px-4 py-2.5 text-right">
                    <div class="flex items-center justify-end gap-2">
                      <button
                        class="text-xs text-blue-400 hover:text-blue-300 transition-colors"
                        @click="editingPasswordUserId = editingPasswordUserId === u.id ? null : u.id; editingPassword = ''; showPassword = false; pwError = ''"
                      >修改密码</button>
                      <span class="text-gray-700">|</span>
                      <button
                        class="text-xs text-red-400 hover:text-red-300 transition-colors disabled:opacity-30"
                        :disabled="u.id === auth.user?.id"
                        :title="u.id === auth.user?.id ? '不能删除自己' : '删除'"
                        @click="deleteUser(u.id)"
                      >删除</button>
                    </div>
                  </td>
                </tr>
                <tr v-if="editingPasswordUserId === u.id" class="bg-gray-800/40">
                  <td colspan="5" class="px-4 py-3">
                    <div class="flex items-center gap-3 flex-wrap">
                      <span class="text-xs text-gray-400 shrink-0">新密码</span>
                      <input
                        v-model="editingPassword"
                        :type="showPassword ? 'text' : 'password'"
                        autocomplete="new-password"
                        class="input py-1 text-sm flex-1 max-w-xs"
                        placeholder="≥ 8 位"
                        @keyup.enter="savePassword(u.id)"
                      />
                      <button
                        type="button"
                        class="text-xs text-gray-500 hover:text-gray-300 px-1 shrink-0"
                        @click="showPassword = !showPassword"
                        tabindex="-1"
                      >{{ showPassword ? '隐藏' : '显示' }}</button>
                      <button
                        class="btn-primary py-1 px-3 text-xs shrink-0"
                        :disabled="changingPassword"
                        @click="savePassword(u.id)"
                      >{{ changingPassword ? '保存中…' : '保存' }}</button>
                      <button
                        class="text-xs text-gray-500 hover:text-gray-300 shrink-0"
                        @click="editingPasswordUserId = null; editingPassword = ''; showPassword = false; pwError = ''"
                      >取消</button>
                      <span v-if="pwSuccess" class="text-xs text-green-400 shrink-0">✓ 密码已修改</span>
                      <span v-if="pwError" class="text-xs text-red-400 shrink-0">{{ pwError }}</span>
                    </div>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>
      </div>

      <!-- ── 操作日志 ── -->
      <div v-if="activeTab === 'logs'">
        <div class="card p-4 mb-4">
          <h2 class="font-semibold text-white mb-3">操作审计日志</h2>
          <!-- 筛选行 -->
          <div class="flex flex-wrap gap-3 mb-4">
            <select
              v-model="auditFilterAction"
              class="input-sm bg-gray-800 border-gray-700 text-gray-200 rounded-lg px-3 py-1.5 text-sm"
            >
              <option value="">全部操作</option>
              <option value="LOGIN">登录成功</option>
              <option value="LOGIN_FAIL">登录失败</option>
              <option value="START_STREAM">开始推流</option>
              <option value="STOP_STREAM">停止推流</option>
              <option value="CREATE_USER">创建用户</option>
              <option value="DELETE_USER">删除用户</option>
              <option value="CHANGE_PASSWORD">重置密码</option>
            </select>
            <input
              v-model="auditFilterUser"
              type="text"
              placeholder="用户名搜索…"
              class="input-sm bg-gray-800 border border-gray-700 text-gray-200 rounded-lg px-3 py-1.5 text-sm w-40 outline-none focus:border-blue-500"
            />
            <button class="btn-sm bg-blue-700 hover:bg-blue-600 text-white rounded-lg px-4 py-1.5 text-sm" @click="auditSearch">查询</button>
            <span class="text-xs text-gray-500 self-center ml-auto">共 {{ auditTotal }} 条记录，第 {{ auditPage }} 页</span>
          </div>

          <!-- 日志表格 -->
          <div class="overflow-x-auto">
            <table class="w-full text-sm text-left">
              <thead>
                <tr class="border-b border-gray-800 text-gray-400 text-xs uppercase tracking-wide">
                  <th class="pb-2 pr-4">时间</th>
                  <th class="pb-2 pr-4">用户</th>
                  <th class="pb-2 pr-4">角色</th>
                  <th class="pb-2 pr-4">操作</th>
                  <th class="pb-2 pr-4">详情</th>
                  <th class="pb-2">IP</th>
                </tr>
              </thead>
              <tbody>
                <tr
                  v-for="log in auditLogs"
                  :key="log.id"
                  class="border-b border-gray-800/50 hover:bg-gray-800/30 transition-colors"
                >
                  <td class="py-2 pr-4 font-mono text-xs text-gray-400 whitespace-nowrap">{{ log.createdAt }}</td>
                  <td class="py-2 pr-4 font-medium text-gray-200">{{ log.username }}</td>
                  <td class="py-2 pr-4 text-gray-400 text-xs">{{ log.role }}</td>
                  <td class="py-2 pr-4">
                    <span class="px-2 py-0.5 rounded text-xs font-medium" :class="auditBadgeClass(log.action)">{{ log.action }}</span>
                  </td>
                  <td class="py-2 pr-4 text-gray-400 text-xs max-w-xs truncate">{{ log.detail }}</td>
                  <td class="py-2 font-mono text-xs text-gray-500">{{ log.ip }}</td>
                </tr>
                <tr v-if="auditLogs.length === 0">
                  <td colspan="6" class="py-8 text-center text-gray-600 text-sm">暂无日志数据</td>
                </tr>
              </tbody>
            </table>
          </div>

          <!-- 分页 -->
          <div class="flex justify-center gap-3 mt-4">
            <button
              class="btn-sm bg-gray-800 hover:bg-gray-700 text-gray-300 rounded-lg px-4 py-1.5 text-sm disabled:opacity-40"
              :disabled="auditPage <= 1"
              @click="auditPrevPage"
            >← 上一页</button>
            <button
              class="btn-sm bg-gray-800 hover:bg-gray-700 text-gray-300 rounded-lg px-4 py-1.5 text-sm disabled:opacity-40"
              :disabled="auditPage * auditPageSize >= auditTotal"
              @click="auditNextPage"
            >下一页 →</button>
          </div>
        </div>
      </div>

    </main>
  </div>
</template>
