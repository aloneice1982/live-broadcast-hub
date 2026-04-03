<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { citiesAPI, type ProcessStatus, type ScheduleItemReq } from '@/api'

const props = defineProps<{
  cityId: number
  scheduleId?: number
  scheduleStatus?: string
  nextStartTime?: string
  scheduleItems?: ScheduleItemReq[]  // 今日排期条目，空闲时用于预览
  manualMode?: boolean
  hasNextItem?: boolean
}>()
const emit = defineEmits<{
  (e: 'start', manual: boolean): void
  (e: 'pause'): void
  (e: 'terminate'): void
  (e: 'advance'): void
  (e: 'reset'): void
}>()

const status = ref<ProcessStatus | null>(null)
const localManual = ref(false)
const starting = ref(false)
let pollingTimer: ReturnType<typeof setInterval>
let countdownTimer: ReturnType<typeof setInterval>

// ── 状态计算 ──────────────────────────────────────────────────

// ffmpeg 正在工作，或调度器 goroutine 正在等待
const isRunning = computed(() =>
  status.value?.status === 'streaming' || status.value?.status === 'warming'
  || props.scheduleStatus === 'running'
)

// 排期等待中（goroutine 等待 start_time，ffmpeg 还没启动）
const isScheduledWaiting = computed(() =>
  props.scheduleStatus === 'running'
  && status.value?.status !== 'streaming'
  && status.value?.status !== 'warming'
)

// 实际推流中（ffmpeg 工作中）
const isStreaming = computed(() =>
  status.value?.status === 'streaming' || status.value?.status === 'warming'
)

// ── 倒计时 ────────────────────────────────────────────────────
const countdownSecs = ref(0)
const autoStartFired = ref(false)

function calcCountdown() {
  if (!props.nextStartTime || localManual.value || isRunning.value || starting.value) {
    countdownSecs.value = 0
    return
  }
  const [h, m] = props.nextStartTime.split(':').map(Number)
  const target = new Date()
  target.setHours(h, m, 0, 0)
  countdownSecs.value = Math.floor((target.getTime() - Date.now()) / 1000)

  if (countdownSecs.value <= 0 && !autoStartFired.value && !isRunning.value && props.scheduleId) {
    autoStartFired.value = true
    emit('start', false)
  }
}

watch(() => props.scheduleId, () => {
  autoStartFired.value = false
})

watch(isRunning, (val) => {
  if (val) starting.value = false
})

const countdownLabel = computed(() => {
  if (!props.nextStartTime || localManual.value || isRunning.value) return null
  if (countdownSecs.value <= 0) return null
  const mins = Math.floor(countdownSecs.value / 60)
  const secs = countdownSecs.value % 60
  return `${String(mins).padStart(2, '0')}:${String(secs).padStart(2, '0')}`
})

// ── API ───────────────────────────────────────────────────────
async function fetchStatus() {
  try {
    const res = await citiesAPI.getStatus(props.cityId)
    status.value = res.data.data
  } catch { /* 静默失败 */ }
}

function handleStart(manual: boolean) {
  starting.value = true
  emit('start', manual)
  setTimeout(() => { starting.value = false }, 10000)
}

function confirmTerminate() {
  if (confirm('确定终止推流并删除本次排期？此操作不可撤销。')) {
    emit('terminate')
  }
}

// ── 辅助显示 ──────────────────────────────────────────────────
const itemTypeIcon: Record<string, string> = {
  live_stream: '📡',
  promo_video: '🎬'
}

onMounted(() => {
  fetchStatus()
  pollingTimer = setInterval(fetchStatus, 3000)
  calcCountdown()
  countdownTimer = setInterval(calcCountdown, 1000)
})
onUnmounted(() => {
  clearInterval(pollingTimer)
  clearInterval(countdownTimer)
})
</script>

<template>
  <div class="card space-y-3">

    <!-- ── 标题栏：状态指示器 ──────────────────────────────── -->
    <div class="flex items-center justify-between">
      <h3 class="text-sm font-semibold text-gray-300">推流控制</h3>
      <span class="flex items-center gap-1.5 text-xs font-medium">
        <template v-if="status?.status === 'streaming'">
          <span class="w-2 h-2 rounded-full bg-green-400" />
          <span class="text-green-400">推流中</span>
        </template>
        <template v-else-if="status?.status === 'warming'">
          <span class="w-2 h-2 rounded-full bg-yellow-400 animate-pulse" />
          <span class="text-yellow-400">预热中</span>
        </template>
        <template v-else-if="isScheduledWaiting">
          <span class="w-2 h-2 rounded-full bg-blue-400 animate-pulse" />
          <span class="text-blue-400">等待开播</span>
        </template>
        <template v-else-if="status?.status === 'breaker_open'">
          <span class="w-2 h-2 rounded-full bg-red-500 animate-ping" />
          <span class="text-red-400">熔断！</span>
        </template>
        <template v-else-if="status?.status === 'failed'">
          <span class="w-2 h-2 rounded-full bg-red-400 animate-pulse" />
          <span class="text-red-400">进程异常</span>
        </template>
        <template v-else>
          <span class="w-2 h-2 rounded-full bg-gray-400" />
          <span class="text-gray-400">空闲</span>
        </template>
      </span>
    </div>

    <!-- ── 熔断告警横幅 ────────────────────────────────────── -->
    <div v-if="status?.status === 'breaker_open'"
         class="rounded-lg bg-red-900/40 border border-red-700 px-4 py-3 flex gap-2 items-start">
      <span class="text-lg leading-none shrink-0">⚠</span>
      <div class="flex-1 min-w-0">
        <p class="text-sm font-semibold text-red-300">熔断触发，推流已停止</p>
        <p class="text-xs mt-0.5 text-red-400">请查看告警记录，确认推流密钥/地址是否过期、网络或直播源是否正常，检查后点「清除熔断」重置</p>
      </div>
      <button
        class="shrink-0 px-2 py-1 rounded-lg border border-red-600 text-red-300 hover:bg-red-900/50 transition-colors text-xs font-medium"
        @click="emit('reset')"
      >清除熔断</button>
    </div>

    <!-- ── 正在推流：当前内容卡 ───────────────────────────── -->
    <template v-if="isStreaming && !isScheduledWaiting">
      <!-- 当前播放内容 -->
      <div class="rounded-lg bg-gray-800/80 border border-gray-700/60 px-4 py-3 space-y-1.5">
        <div class="flex items-start justify-between gap-2">
          <div class="min-w-0">
            <p class="text-xs text-gray-500 mb-0.5">正在播放</p>
            <p class="text-sm font-semibold text-white leading-snug truncate">
              {{ status?.currentItemName ?? '—' }}
            </p>
          </div>
          <!-- 模式 badge -->
          <span v-if="manualMode !== undefined"
                class="shrink-0 text-xs px-2 py-0.5 rounded-full"
                :class="manualMode ? 'bg-orange-900/40 text-orange-300 border border-orange-700' : 'bg-blue-900/40 text-blue-300 border border-blue-700'">
            {{ manualMode ? '手动' : '自动' }}
          </span>
        </div>
        <!-- 进度 -->
        <p v-if="status?.currentItemIndex && status?.todayItemCount"
           class="text-xs text-gray-400">
          第 {{ status.currentItemIndex }} 段 / 共 {{ status.todayItemCount }} 段
        </p>
      </div>

      <!-- 下一段预览 -->
      <div v-if="status?.nextItemTime"
           class="flex items-center gap-2 px-3 py-2 rounded-lg bg-gray-900/60 border border-gray-800 text-xs text-gray-400">
        <span class="text-gray-600 shrink-0">下一段</span>
        <span class="font-mono text-blue-400 shrink-0">{{ status.nextItemTime }}</span>
        <span class="truncate">{{ status.nextItemName ?? '—' }}</span>
      </div>

      <!-- 操作按钮 -->
      <div class="flex gap-2">
        <button
          v-if="manualMode && hasNextItem"
          class="btn-primary flex-1"
          @click="emit('advance')"
        >⏭ 下一段</button>
        <button class="btn-ghost flex-1" @click="emit('pause')">⏸ 暂停</button>
        <button
          class="flex-1 px-3 py-2 rounded-lg border border-red-700/60 text-red-400 hover:bg-red-900/30 transition-colors text-sm font-medium"
          @click="confirmTerminate"
        >⛔ 终止</button>
      </div>
    </template>

    <!-- ── 等待开播 ────────────────────────────────────────── -->
    <template v-else-if="isScheduledWaiting">
      <div class="rounded-lg bg-blue-900/20 border border-blue-700/50 px-4 py-3 flex gap-2 items-center">
        <span class="animate-pulse text-base shrink-0">⏳</span>
        <p class="flex-1 text-xs text-blue-300 min-w-0">
          排期等待中，将在
          <span class="font-mono font-semibold">{{ nextStartTime }}</span>
          自动开始推流
        </p>
        <button
          class="shrink-0 px-2 py-1 rounded-lg border border-blue-600 text-blue-300 hover:bg-blue-900/50 transition-colors text-xs font-medium disabled:opacity-50"
          :disabled="starting"
          @click="handleStart(false)"
        >{{ starting ? '启动中…' : '⚡ 立即推流' }}</button>
      </div>
      <div class="flex gap-2">
        <button class="btn-ghost flex-1" @click="emit('pause')">⏸ 取消等待</button>
        <button
          class="flex-1 px-3 py-2 rounded-lg border border-red-700/60 text-red-400 hover:bg-red-900/30 transition-colors text-sm font-medium"
          @click="confirmTerminate"
        >⛔ 终止</button>
      </div>
    </template>

    <!-- ── 空闲：排期预览 + 启动 ────────────────────────── -->
    <template v-else-if="!isRunning">
      <!-- 今日排期简要时间轴（最多显示4条） -->
      <div v-if="scheduleItems && scheduleItems.length > 0"
           class="flex items-center gap-1 flex-wrap py-1">
        <span class="text-xs text-gray-500 shrink-0">今日排期</span>
        <div
          v-for="(item, i) in scheduleItems.slice(0, 4)"
          :key="i"
          class="flex items-center gap-1 bg-gray-800/60 rounded px-2 py-0.5 text-xs"
        >
          <span class="font-mono text-blue-400">{{ item.startTime }}</span>
          <span>{{ itemTypeIcon[item.itemType] ?? '▪' }}</span>
        </div>
        <span v-if="scheduleItems.length > 4" class="text-xs text-gray-600">+{{ scheduleItems.length - 4 }}</span>
      </div>
      <p v-else-if="!scheduleId" class="text-xs text-gray-500 text-center py-1">
        请先在「排期编排」中创建并保存排期
      </p>

      <!-- 模式切换 -->
      <div class="flex items-center gap-3 py-1.5 px-3 bg-gray-800/60 rounded-lg">
        <span class="text-xs text-gray-400 shrink-0">推流模式</span>
        <label class="flex items-center gap-1.5 cursor-pointer select-none">
          <input type="checkbox" v-model="localManual" class="accent-blue-500 w-3.5 h-3.5" />
          <span class="text-xs font-medium" :class="localManual ? 'text-orange-300' : 'text-blue-300'">
            {{ localManual ? '手动' : '自动' }}
          </span>
        </label>
        <span class="text-xs text-gray-500">
          {{ localManual ? '立即启动，手动切段' : '按排期时间自动切换' }}
        </span>
      </div>

      <!-- 启动按钮 + 倒计时 -->
      <div class="flex items-center gap-2">
        <button
          class="btn-primary flex-1"
          :disabled="!scheduleId || starting"
          @click="handleStart(localManual)"
        >{{ starting ? '启动中…' : '⚡ 立即推流' }}</button>
        <div v-if="countdownLabel"
             class="shrink-0 flex flex-col items-center bg-gray-800/80 rounded-lg px-3 py-1.5 border border-gray-700/60">
          <span class="text-xs text-gray-500 leading-none mb-0.5">自动开始</span>
          <span class="font-mono text-base font-bold text-blue-400 leading-none">{{ countdownLabel }}</span>
        </div>
      </div>
    </template>

  </div>
</template>
