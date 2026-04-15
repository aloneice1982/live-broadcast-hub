<script setup lang="ts">
/**
 * CityConsole.vue — 地市管理员一页式操作台（MVP 直推模式）
 */
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  citiesAPI, streamSourcesAPI, streamConfigAPI, alertsAPI, videosAPI,
  type City, type ProcessStatus, type StreamSource, type StreamConfig, type AlertLog, type PromoVideo
} from '@/api'
import { useAuthStore } from '@/stores/auth'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const cityId = computed(() => Number(route.params.cityId))
const city = ref<City | null>(null)
const streamSources = ref<StreamSource[]>([])
const streamConfig = ref<StreamConfig | null>(null)
const alerts = ref<AlertLog[]>([])
const pageLoading = ref(true)

// ── 默认推流地址 ──────────────────────────────────────────────
const DEFAULT_PUSH_URL = 'rtmp://111583.livepush.myqcloud.com/trtc_1400439699/'

// ── 日期工具（UTC+8 安全）────────────────────────────────────
function localDateString(d = new Date()): string {
  const y = d.getFullYear()
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${y}-${m}-${day}`
}

// ── 状态轮询 ──────────────────────────────────────────────────
const currentStatus = ref<ProcessStatus | null>(null)
let pollingTimer: ReturnType<typeof setInterval>

async function fetchStatus() {
  try {
    const res = await citiesAPI.getStatus(cityId.value)
    currentStatus.value = res.data.data
  } catch { /* 静默 */ }
}

const isStreaming = computed(() =>
  currentStatus.value?.status === 'streaming' || currentStatus.value?.status === 'warming'
)
const isBreakerOpen = computed(() => currentStatus.value?.status === 'breaker_open')

// ── 推流计时器（1 秒 tick）────────────────────────────────────
const timerTick = ref(0)
let timerInterval: ReturnType<typeof setInterval>

const streamingDuration = computed(() => {
  timerTick.value  // reactive dependency — 每秒重新计算
  const t = currentStatus.value?.lastStartedAt
  if (!t || !isStreaming.value) return ''
  // 规范化时间字符串：剥除所有时区后缀（含 go-sqlite3 误加的 Z），统一以 +08:00 解析
  const bare = t.replace('Z', '').replace(/[+-]\d{2}:\d{2}$/, '')
  const normalized = bare.includes('T') ? bare : bare.replace(' ', 'T')
  const startMs = new Date(normalized + '+08:00').getTime()
  if (isNaN(startMs)) return ''
  const secs = Math.max(0, Math.floor((Date.now() - startMs) / 1000))
  const h = Math.floor(secs / 3600)
  const m = Math.floor((secs % 3600) / 60)
  const s = secs % 60
  return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
})

// ── 今日直播源 ────────────────────────────────────────────────
const today = localDateString()
const todaySources = computed(() =>
  streamSources.value.filter(s =>
    s.isActive && (!s.matchDatetime || s.matchDatetime.startsWith(today))
  )
)

// ── 直推状态 ──────────────────────────────────────────────────
const selectedSourceId = ref<number | null>(null)
const pushing = ref(false)
const pushError = ref('')
const currentSourceName = ref('')

// 选中的直播源详情（用于预览卡）
const selectedSource = computed(() =>
  todaySources.value.find(s => s.id === selectedSourceId.value) ?? null
)

// ── 推流配置 ──────────────────────────────────────────────────
const configForm = ref({ pushUrl: '', pushKey: '', volumeGain: 1.5 })
const savingConfig = ref(false)
const configMsg = ref('')
const configExpanded = ref(true)  // 默认展开，方便首次配置
const configEditing = ref(false)  // 本地编辑模式（仅展开表单，不改 DB 锁定状态）
const configLocked = computed(() => streamConfig.value?.configLocked ?? false)

watch(streamConfig, cfg => {
  if (cfg) {
    configForm.value.pushUrl = cfg.pushUrl || DEFAULT_PUSH_URL
    configForm.value.pushKey = cfg.pushKey ?? ''
    configForm.value.volumeGain = cfg.volumeGain ?? 1.5
  }
})

// ── 静音状态 ──────────────────────────────────────────────────
const isMuted = ref(false)
const mutingSwitching = ref(false)
const showBandwidthModal = ref(false)

// ── 宣传片插播 ────────────────────────────────────────────────
const promoVideos = ref<PromoVideo[]>([])
const selectedPromoId = ref<number | null>(null)
const insertingPromo = ref(false)
const promoError = ref('')

async function toggleMute() {
  mutingSwitching.value = true
  try {
    await citiesAPI.setMute(cityId.value, !isMuted.value)
    isMuted.value = !isMuted.value
  } catch (e: any) {
    pushError.value = e.response?.data?.error ?? '静音切换失败'
  } finally { mutingSwitching.value = false }
}

// ── 加载 ──────────────────────────────────────────────────────
async function loadAll() {
  pageLoading.value = true
  const [citiesRes, sourcesRes, configRes, videosRes] = await Promise.allSettled([
    citiesAPI.list(),
    streamSourcesAPI.list(),
    streamConfigAPI.get(cityId.value),
    videosAPI.list(cityId.value),
  ])

  if (citiesRes.status === 'fulfilled')
    city.value = citiesRes.value.data.data?.find(c => c.id === cityId.value) ?? null
  if (sourcesRes.status === 'fulfilled')
    streamSources.value = sourcesRes.value.data.data ?? []
  if (configRes.status === 'fulfilled')
    streamConfig.value = configRes.value.data.data
  if (videosRes.status === 'fulfilled')
    promoVideos.value = (videosRes.value.data.data ?? []).filter(v => v.transcodeStatus === 'done')

  await Promise.all([fetchStatus(), loadAlerts()])
  pageLoading.value = false
}

async function loadAlerts() {
  try {
    const res = await alertsAPI.list(cityId.value)
    alerts.value = res.data.data ?? []
  } catch { /* 静默 */ }
}

watch(cityId, loadAll, { immediate: false })
onMounted(() => {
  loadAll()
  pollingTimer = setInterval(fetchStatus, 3000)
  timerInterval = setInterval(() => timerTick.value++, 1000)
})
onUnmounted(() => {
  clearInterval(pollingTimer)
  clearInterval(timerInterval)
})

// ── 直推操作 ──────────────────────────────────────────────────
async function startDirectPush() {
  if (!selectedSourceId.value) {
    pushError.value = '请选择直播源'
    return
  }
  if (!configForm.value.pushUrl) {
    pushError.value = '请先填写并保存推流地址'
    configExpanded.value = true
    return
  }
  pushing.value = true
  pushError.value = ''
  try {
    const res = await citiesAPI.directPush(cityId.value, selectedSourceId.value)
    currentSourceName.value = res.data.data.sourceName
    // Loading 防呆：等待后端真正进入 streaming 状态（最多 10s）
    for (let i = 0; i < 10; i++) {
      await new Promise(r => setTimeout(r, 1000))
      await fetchStatus()
      if (isStreaming.value) break
    }
  } catch (e: any) {
    if (e.response?.status === 503 &&
        e.response?.data?.error === 'SERVER_BANDWIDTH_FULL') {
      showBandwidthModal.value = true
    } else {
      pushError.value = e.response?.data?.error ?? '启动失败'
    }
  } finally {
    pushing.value = false
  }
}

async function stopPush() {
  await citiesAPI.resetFFmpeg(cityId.value)
  currentSourceName.value = ''
  isMuted.value = false
  await fetchStatus()
  await loadAlerts()
}

async function clearAlertsAndStatus() {
  if (!confirm('清除所有告警并重置推流状态？')) return
  await alertsAPI.clear(cityId.value)
  isMuted.value = false
  await Promise.all([fetchStatus(), loadAlerts()])
}

// ── 配置保存 ──────────────────────────────────────────────────
async function saveConfig() {
  savingConfig.value = true
  configMsg.value = ''
  try {
    await streamConfigAPI.update(cityId.value, {
      pushUrl: configForm.value.pushUrl || DEFAULT_PUSH_URL,
      pushKey: configForm.value.pushKey,
      volumeGain: configForm.value.volumeGain
    })
    const res = await streamConfigAPI.get(cityId.value)
    streamConfig.value = res.data.data
    configEditing.value = false
    configMsg.value = '✓ 已保存并锁定'
    setTimeout(() => { configMsg.value = '' }, 3000)
  } catch (e: any) {
    configMsg.value = '失败：' + (e.response?.data?.error ?? e.message)
  } finally { savingConfig.value = false }
}

function logout() { auth.logout(); router.push('/login') }

async function insertPromo() {
  if (!selectedPromoId.value) return
  insertingPromo.value = true
  promoError.value = ''
  try {
    await citiesAPI.insertPromo(cityId.value, selectedPromoId.value)
    selectedPromoId.value = null
  } catch (e: any) {
    promoError.value = e.response?.data?.error ?? '插播失败'
  } finally {
    insertingPromo.value = false
  }
}

function formatAlertMsg(msg: string): string {
  return msg
    .replace('[push]', '推流进程')
    .replace('[inject]', '注入进程')
}

const statusDotClass: Record<string, string> = {
  streaming:    'bg-green-400',
  warming:      'bg-yellow-400 animate-pulse',
  failed:       'bg-red-400 animate-pulse',
  breaker_open: 'bg-red-500 animate-ping',
  idle:         'bg-gray-400',
}
const statusLabel: Record<string, string> = {
  streaming:    '推流中',
  warming:      '预热中',
  failed:       '进程异常',
  breaker_open: '熔断！',
  idle:         '空闲',
}
</script>

<template>
  <div class="min-h-screen bg-gray-950 flex flex-col">
    <!-- 顶栏 -->
    <header class="border-b border-gray-800 bg-gray-900/80 backdrop-blur sticky top-0 z-20">
      <div class="max-w-3xl mx-auto px-4 h-14 flex items-center justify-between gap-4">
        <div class="flex items-center gap-2 min-w-0">
          <button
            v-if="auth.user?.role === 'super_admin'"
            class="btn-ghost text-sm shrink-0"
            @click="router.push('/dashboard')"
          >← 返回大盘</button>
          <span class="text-xl shrink-0">📡</span>
          <div class="min-w-0">
            <span class="font-bold text-white text-sm">苏超直播中台</span>
            <span v-if="city" class="ml-2 text-blue-400 font-medium text-sm">{{ city.name }}管理台</span>
          </div>
        </div>
        <div class="flex items-center gap-2 shrink-0">
          <span v-if="isBreakerOpen" class="badge bg-red-900 text-red-300 animate-pulse">⚠ 熔断</span>
          <button class="btn-ghost text-sm" @click="logout">退出</button>
        </div>
      </div>
    </header>

    <!-- 主内容区 -->
    <main class="flex-1 max-w-3xl mx-auto w-full px-4 py-6 space-y-4">

      <!-- 加载占位 -->
      <div v-if="pageLoading" class="flex items-center justify-center py-24">
        <div class="text-center space-y-3">
          <div class="w-8 h-8 border-2 border-blue-500 border-t-transparent rounded-full animate-spin mx-auto" />
          <p class="text-sm text-gray-500">加载中…</p>
        </div>
      </div>

      <template v-else>

        <!-- ── 推流配置（可折叠，默认展开）──────────────────── -->
        <div class="card space-y-3">
          <button class="flex items-center justify-between w-full" @click="configExpanded = !configExpanded">
            <h3 class="text-sm font-semibold text-gray-300">推流配置</h3>
            <span class="text-xs flex items-center gap-2">
              <span v-if="streamConfig?.pushUrl" class="text-green-400 font-mono truncate max-w-[200px]">
                {{ streamConfig.pushUrl.replace(/^rtmp:\/\//, '') }}
              </span>
              <span v-else class="text-yellow-400">⚠ 未配置</span>
              <span class="text-gray-500">{{ configExpanded ? '▲' : '▼' }}</span>
            </span>
          </button>

          <template v-if="configExpanded">
            <!-- 已锁定且不在编辑模式：折叠显示 -->
            <div v-if="configLocked && !configEditing"
                 class="flex items-center justify-between px-4 py-2.5 rounded-lg bg-green-900/20 border border-green-700/50">
              <span class="text-sm text-green-400 font-medium">🔒 推流密钥已就绪</span>
              <button class="text-xs text-gray-400 hover:text-gray-200 transition-colors"
                      @click="configEditing = true">修改</button>
            </div>

            <!-- 未锁定或正在编辑：显示完整表单 -->
            <template v-else>
              <div class="grid grid-cols-1 sm:grid-cols-2 gap-3 pt-1">
                <div class="sm:col-span-2">
                  <label class="label">微信视频号推流地址</label>
                  <input v-model="configForm.pushUrl" class="input" :placeholder="DEFAULT_PUSH_URL" />
                </div>
                <div class="sm:col-span-2">
                  <label class="label">推流密钥</label>
                  <input v-model="configForm.pushKey" type="text" class="input font-mono text-sm" placeholder="请输入推流密钥" />
                </div>
                <div>
                  <label class="label">
                    音量增益：<span class="text-blue-400 font-mono">{{ configForm.volumeGain.toFixed(1) }}x</span>
                  </label>
                  <input v-model.number="configForm.volumeGain" type="range" min="1.0" max="2.0" step="0.1" class="w-full accent-blue-500 mt-1" />
                  <p class="text-xs text-gray-500 mt-1">注：直播过程中调整音量将在下次推流时生效</p>
                </div>
                <div class="bg-gray-800/60 rounded-lg px-3 py-2.5">
                  <p class="text-xs font-medium text-gray-400 mb-1">SRS 内网挂载点（只读）</p>
                  <p class="text-xs font-mono text-gray-300">
                    rtmp://srs:1935/{{ streamConfig?.srsApp ?? 'live' }}/{{ streamConfig?.srsStream ?? '—' }}
                  </p>
                </div>
              </div>
              <div class="flex items-center gap-3">
                <button class="btn-primary" :disabled="!configForm.pushKey || savingConfig" @click="saveConfig">
                  {{ savingConfig ? '保存中…' : '保存' }}
                </button>
                <span v-if="!configForm.pushKey && !savingConfig" class="text-xs text-yellow-400">填写密钥后保存即锁定</span>
                <span v-if="configMsg" class="text-xs" :class="configMsg.startsWith('✓') ? 'text-green-400' : 'text-red-400'">
                  {{ configMsg }}
                </span>
              </div>
            </template>
          </template>
        </div>

        <!-- ── 一键直推 ─────────────────────────────────────── -->
        <div class="card space-y-3">

          <!-- 状态指示器 -->
          <div class="flex items-center justify-between">
            <h3 class="text-sm font-semibold text-gray-300">推流控制</h3>
            <span class="flex items-center gap-1.5 text-xs font-medium">
              <span class="w-2 h-2 rounded-full" :class="statusDotClass[currentStatus?.status ?? 'idle']" />
              <span :class="{
                'text-green-400': currentStatus?.status === 'streaming',
                'text-yellow-400': currentStatus?.status === 'warming',
                'text-red-400': currentStatus?.status === 'failed' || currentStatus?.status === 'breaker_open',
                'text-gray-400': !currentStatus || currentStatus.status === 'idle',
              }">{{ statusLabel[currentStatus?.status ?? 'idle'] }}</span>
            </span>
          </div>

          <!-- 熔断告警横幅 -->
          <div v-if="isBreakerOpen"
               class="rounded-lg bg-red-900/40 border border-red-700 px-4 py-3 flex gap-2 items-start">
            <span class="text-lg leading-none shrink-0">⚠</span>
            <div class="flex-1 min-w-0">
              <p class="text-sm font-semibold text-red-300">熔断触发，推流已停止</p>
              <p class="text-xs mt-0.5 text-red-400">请查看告警记录，确认推流密钥/地址是否过期、网络或直播源是否正常，检查后点「清除熔断」重置</p>
            </div>
            <button
              class="shrink-0 px-2 py-1 rounded-lg border border-red-600 text-red-300 hover:bg-red-900/50 transition-colors text-xs font-medium"
              @click="stopPush"
            >清除熔断</button>
          </div>

          <!-- 推流配置未完成提示 -->
          <div v-if="!configLocked"
               class="rounded-lg bg-yellow-900/30 border border-yellow-800 px-4 py-2.5 text-sm text-yellow-300 flex gap-2 items-center">
            <span>⚠</span>
            <span>推流配置未锁定，
              <button class="underline" @click="configExpanded = true; configEditing = true">请填写并保存推流密钥 →</button>
            </span>
          </div>

          <!-- 推流中：状态面板 + 静音 + 停止 -->
          <template v-if="isStreaming">
            <div class="rounded-lg bg-gray-800/80 border border-gray-700/60 px-4 py-3">
              <div class="flex items-center justify-between gap-2">
                <div class="min-w-0">
                  <p class="text-xs text-gray-500 mb-0.5">正在推流</p>
                  <p class="text-sm font-semibold text-white truncate">
                    {{ currentSourceName || currentStatus?.currentItemName || '直播中…' }}
                  </p>
                </div>
                <!-- 推流时长计时器 -->
                <span v-if="streamingDuration" class="font-mono text-green-300 text-sm shrink-0 tabular-nums">
                  {{ streamingDuration }}
                </span>
              </div>
            </div>

            <!-- 静音切换 -->
            <div class="flex flex-col gap-1">
              <div class="flex items-center justify-between px-1">
                <span class="text-xs text-gray-400">音量</span>
                <button
                  class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg border text-xs font-medium transition-colors"
                  :class="isMuted
                    ? 'border-yellow-600 text-yellow-300 bg-yellow-900/20'
                    : 'border-gray-600 text-gray-300 hover:bg-gray-700/50'"
                  :disabled="mutingSwitching"
                  @click="toggleMute"
                >{{ isMuted ? '🔇 恢复' : '🔊 静音' }}</button>
              </div>
              <p class="text-xs text-gray-600 px-1 text-right">切换静音可能导致画面短暂卡顿 1-2 秒</p>
            </div>

            <button
              class="w-full px-4 py-3 rounded-lg border border-red-700/60 text-red-400 hover:bg-red-900/30 transition-colors text-sm font-semibold"
              @click="stopPush"
            >⏹ 停止推流</button>

            <!-- 宣传片插播 -->
            <div class="border-t border-gray-700/50 pt-3 space-y-2">
              <p class="text-xs font-medium text-gray-400">📺 插播宣传片</p>

              <!-- 插播中状态 -->
              <div v-if="currentStatus?.promoInserting"
                   class="rounded-lg bg-purple-900/30 border border-purple-700/60 px-3 py-2.5 flex items-center gap-2">
                <span class="w-2 h-2 rounded-full bg-purple-400 animate-pulse shrink-0" />
                <div>
                  <span class="text-sm font-medium text-purple-300">宣传片播放中</span>
                  <span class="text-xs text-purple-500 ml-2">播完后自动切回直播流</span>
                </div>
              </div>

              <!-- 插播选择（非插播中时显示） -->
              <template v-else>
                <div v-if="promoVideos.length === 0" class="text-xs text-gray-500">
                  暂无已转码宣传片，请在排期管理中上传
                </div>
                <template v-else>
                  <select v-model="selectedPromoId" class="input text-sm">
                    <option :value="null">— 选择宣传片 —</option>
                    <option v-for="v in promoVideos" :key="v.id" :value="v.id">
                      {{ v.displayName || v.originalFilename }}{{ v.durationSeconds ? ` (${v.durationSeconds}秒)` : '' }}
                    </option>
                  </select>
                  <p v-if="promoError" class="text-xs text-red-400">⚠ {{ promoError }}</p>
                  <button
                    class="btn-primary w-full"
                    :disabled="!selectedPromoId || insertingPromo"
                    @click="insertPromo"
                  >{{ insertingPromo ? '插播中…' : '⚡ 立即插播' }}</button>
                  <p class="text-xs text-gray-600 text-center">播放完成后自动切回直播流</p>
                </template>
              </template>
            </div>
          </template>

          <!-- 空闲：直播源选择 + 启动 -->
          <template v-else-if="!isBreakerOpen">
            <div>
              <label class="label">选择今日直播源</label>
              <select v-model="selectedSourceId" class="input">
                <option :value="null" disabled>— 请选择 —</option>
                <option v-for="s in todaySources" :key="s.id" :value="s.id">{{ s.name }}</option>
              </select>
              <p v-if="todaySources.length === 0" class="text-xs text-gray-500 mt-1">
                今日无可用直播源，请在超管大盘"直播源管理"中添加
              </p>
            </div>

            <!-- 选中直播源的详情预览卡 -->
            <div v-if="selectedSource && (selectedSource.matchDatetime || selectedSource.round || selectedSource.channel)"
                 class="rounded-lg bg-gray-800/60 border border-gray-700/50 px-3 py-2.5 text-xs space-y-1">
              <div v-if="selectedSource.matchDatetime" class="flex gap-2">
                <span class="text-gray-500 shrink-0 w-14">比赛时间</span>
                <span class="text-gray-200">{{ selectedSource.matchDatetime }}</span>
              </div>
              <div v-if="selectedSource.round" class="flex gap-2">
                <span class="text-gray-500 shrink-0 w-14">轮次</span>
                <span class="text-gray-200">{{ selectedSource.round }}</span>
              </div>
              <div v-if="selectedSource.channel" class="flex gap-2">
                <span class="text-gray-500 shrink-0 w-14">频道</span>
                <span class="text-gray-200">{{ selectedSource.channel }}</span>
              </div>
            </div>

            <p v-if="pushError" class="text-xs text-red-400">⚠ {{ pushError }}</p>

            <button
              class="btn-primary w-full py-3 text-base font-semibold disabled:opacity-40"
              :disabled="!selectedSourceId || !configLocked || pushing"
              @click="startDirectPush"
            >
              {{ pushing ? '启动中…' : '⚡ 开始推流' }}
            </button>

            <p v-if="!configLocked" class="text-xs text-center text-yellow-400">
              请先保存推流密钥后才能开播
            </p>

          </template>

        </div>

        <!-- ── 告警记录 ─────────────────────────────────────── -->
        <div class="card space-y-3">
          <div class="flex items-center justify-between">
            <h3 class="text-sm font-semibold text-gray-300">告警记录</h3>
            <div class="flex items-center gap-3">
              <button class="text-xs text-gray-500 hover:text-gray-300" @click="loadAlerts">刷新</button>
              <button
                class="text-xs text-red-400 hover:text-red-300 transition-colors"
                @click="clearAlertsAndStatus"
              >清除熔断 / 告警</button>
            </div>
          </div>
          <div class="space-y-2 max-h-72 overflow-y-auto">
            <div
              v-for="a in alerts"
              :key="a.id"
              class="text-xs rounded-lg px-3 py-2 border"
              :class="{
                'border-yellow-800/50 bg-yellow-900/20 text-yellow-300': a.level === 'warn',
                'border-red-800/50 bg-red-900/20 text-red-300': a.level === 'error',
                'border-red-700 bg-red-900/30 text-red-200': a.level === 'critical'
              }"
            >
              <div class="flex items-center gap-2 mb-0.5">
                <span class="font-medium uppercase">{{ a.level }}</span>
                <span class="text-gray-500">{{ new Date(a.createdAt).toLocaleTimeString('zh-CN') }}</span>
                <span v-if="a.smsSent" class="badge bg-green-900/40 text-green-400 text-[10px]">短信已发</span>
              </div>
              <p class="leading-relaxed">{{ formatAlertMsg(a.message) }}</p>
            </div>
            <p v-if="alerts.length === 0" class="text-gray-500 text-center py-4">暂无告警记录</p>
          </div>
        </div>

      </template>
    </main>
  </div>

  <!-- 带宽限制弹窗 -->
  <Teleport to="body">
    <div v-if="showBandwidthModal"
         class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm">
      <div class="bg-gray-900 border border-gray-700 rounded-2xl p-6 max-w-sm w-full mx-4 shadow-2xl">
        <div class="flex items-start gap-3 mb-4">
          <span class="text-2xl leading-none shrink-0">⚠️</span>
          <div>
            <h3 class="font-semibold text-white mb-1">服务器资源已满</h3>
            <p class="text-sm text-gray-400">当前服务器带宽已达上限，请稍后再试。</p>
          </div>
        </div>
        <button class="btn-primary w-full" @click="showBandwidthModal = false">知道了</button>
      </div>
    </div>
  </Teleport>
</template>
