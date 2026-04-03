<script setup lang="ts">
/**
 * Dashboard.vue — 超管全省大盘
 */
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import {
  citiesAPI, streamSourcesAPI, usersAPI, systemAPI,
  type City, type ProcessStatus, type StreamSource, type User
} from '@/api'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const auth = useAuthStore()
const activeTab = ref<'overview' | 'sources' | 'users'>('overview')

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

function logout() { auth.logout(); router.push('/login') }

onMounted(() => {
  loadCities(); loadSources(); loadUsers(); fetchBandwidth()
  pollingTimer = setInterval(fetchAllStatuses, 5000)
  tickTimer = setInterval(() => tick.value++, 1000)
  bandwidthTimer = setInterval(fetchBandwidth, 5000)
})
onUnmounted(() => {
  clearInterval(pollingTimer)
  clearInterval(tickTimer)
  clearInterval(bandwidthTimer)
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
          <span class="badge bg-blue-900/50 text-blue-300">超管大盘</span>
        </div>
        <button class="btn-ghost text-sm" @click="logout">退出登录</button>
      </div>
    </header>

    <main class="max-w-7xl mx-auto px-4 py-6">
      <!-- 选项卡（隐藏"排期管理"） -->
      <div class="flex gap-1 mb-6 bg-gray-900 border border-gray-800 rounded-xl p-1 w-fit">
        <button
          v-for="tab in [{ key: 'overview', label: '全省大盘' }, { key: 'sources', label: '直播源管理' }, { key: 'users', label: '用户管理' }]"
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
            class="card cursor-pointer border transition-all hover:scale-[1.02] h-28 flex flex-col justify-between overflow-hidden"
            :class="statusBorder[displayStatus(city.id)]"
            @click="router.push(`/city/${city.id}`)"
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
                  v-if="statuses[city.id]?.status === 'breaker_open'"
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
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-800">
              <tr v-for="u in users" :key="u.id" class="hover:bg-gray-800/50 transition-colors">
                <td class="px-4 py-2.5 font-medium text-white">{{ u.username }}</td>
                <td class="px-4 py-2.5">
                  <span :class="u.role === 'super_admin' ? 'badge bg-blue-900/50 text-blue-300' : 'badge bg-gray-700 text-gray-300'">
                    {{ u.role === 'super_admin' ? '超管' : '地市' }}
                  </span>
                </td>
                <td class="px-4 py-2.5 text-gray-400">{{ cities.find(c => c.id === u.cityId)?.name ?? '—' }}</td>
                <td class="px-4 py-2.5 text-gray-400 font-mono text-xs">{{ u.phone ?? '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </main>
  </div>
</template>
