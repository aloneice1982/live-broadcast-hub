<script setup lang="ts">
/**
 * Timeline.vue — 可视化时间轴排期编辑器
 *
 * 功能：
 *  - 添加/删除排期条目（宣传片 或 直播源）
 *  - 拖拽排序（HTML5 Drag API）
 *  - 时间重叠实时检测，高亮冲突行
 *  - v-model 双向绑定 ScheduleItemReq[]
 */
import { ref, computed, watch } from 'vue'
import type { PromoVideo, StreamSource, ScheduleItemReq } from '@/api'

const props = defineProps<{
  modelValue: ScheduleItemReq[]
  videos: PromoVideo[]        // 已转码完成的宣传片列表
  streamSources: StreamSource[] // 可用直播源列表
  scheduleDate: string        // YYYY-MM-DD，用于过滤直播源
  cityCode: string            // 本地市 code，如 "tz"，优先排序
  disabled?: boolean
}>()
const emit = defineEmits<{
  (e: 'update:modelValue', v: ScheduleItemReq[]): void
}>()

// 本地副本，避免直接修改 props
const items = ref<ScheduleItemReq[]>(props.modelValue.map(i => ({ ...i })))

watch(() => props.modelValue, val => {
  items.value = val.map(i => ({ ...i }))
}, { deep: true })

function commit() {
  emit('update:modelValue', items.value.map((it, idx) => ({ ...it, orderIndex: idx })))
}

// ── 添加条目 ──────────────────────────────────────────────────
function addItem() {
  items.value.push({
    orderIndex: items.value.length,
    itemType: 'promo_video',
    startTime: nextDefaultTime(),
    loopCount: -1
  })
  commit()
}

function nextDefaultTime(): string {
  if (items.value.length === 0) return '19:00'
  const last = items.value[items.value.length - 1].startTime
  const [h, m] = last.split(':').map(Number)
  const next = new Date(0, 0, 0, h, m + 60)
  return `${String(next.getHours()).padStart(2, '0')}:${String(next.getMinutes()).padStart(2, '0')}`
}

// ── 删除条目 ──────────────────────────────────────────────────
function removeItem(idx: number) {
  items.value.splice(idx, 1)
  commit()
}

// ── 拖拽排序 ──────────────────────────────────────────────────
const dragIdx = ref<number | null>(null)

function onDragStart(idx: number) { dragIdx.value = idx }
function onDragOver(e: DragEvent, idx: number) {
  e.preventDefault()
  if (dragIdx.value === null || dragIdx.value === idx) return
  const moved = items.value.splice(dragIdx.value, 1)[0]
  items.value.splice(idx, 0, moved)
  dragIdx.value = idx
}
function onDragEnd() { dragIdx.value = null; commit() }

// ── 时间重叠检测 ──────────────────────────────────────────────
const conflictIndices = computed<Set<number>>(() => {
  const seen = new Map<string, number>()
  const conflicts = new Set<number>()
  items.value.forEach((item, idx) => {
    if (!item.startTime) return
    if (seen.has(item.startTime)) {
      conflicts.add(seen.get(item.startTime)!)
      conflicts.add(idx)
    } else {
      seen.set(item.startTime, idx)
    }
  })
  return conflicts
})

const hasConflict = computed(() => conflictIndices.value.size > 0)

// ── 工具函数 ──────────────────────────────────────────────────
const readyVideos = computed(() => props.videos.filter(v => v.transcodeStatus === 'done'))

// 直播源：先按日期过滤，再按本地市优先排序
const activeStreams = computed(() => {
  const all = props.streamSources.filter(s => s.isActive)
  const isLocal = (s: StreamSource) =>
    (s.name?.includes(props.cityCode) || s.channel?.includes(props.cityCode)) ?? false

  if (props.scheduleDate) {
    const dated = all.filter(s => s.matchDatetime?.startsWith(props.scheduleDate))
    const undated = all.filter(s => !s.matchDatetime)
    // 有日期的：本地优先，其余次之；无日期的附后
    return [
      ...dated.filter(isLocal),
      ...dated.filter(s => !isLocal(s)),
      ...undated,
    ]
  }
  // 无日期条件：本地优先
  return [...all.filter(isLocal), ...all.filter(s => !isLocal(s))]
})

function videoLabel(id?: number) {
  const v = props.videos.find(v => v.id === id)
  return v ? `🎬 ${v.originalFilename}` : '—'
}
function streamLabel(id?: number) {
  const s = props.streamSources.find(s => s.id === id)
  return s ? `📡 ${s.name}` : '—'
}
function durationLabel(v?: PromoVideo) {
  if (!v?.durationSeconds) return ''
  const m = Math.floor(v.durationSeconds / 60)
  const s = v.durationSeconds % 60
  return `${m}:${String(s).padStart(2, '0')}`
}
</script>

<template>
  <div class="space-y-3">
    <!-- 冲突警告 -->
    <div v-if="hasConflict"
         class="rounded-lg bg-yellow-900/40 border border-yellow-700 px-4 py-2.5 text-sm text-yellow-300 flex items-center gap-2">
      <span>⚠</span>
      <span>存在时间重叠的排期条目，请修正后再保存。</span>
    </div>

    <!-- 时间轴列表 -->
    <div class="space-y-2">
      <TransitionGroup name="list">
        <div
          v-for="(item, idx) in items"
          :key="idx"
          class="group flex items-start gap-2 p-3 rounded-xl border transition-colors"
          :class="[
            conflictIndices.has(idx)
              ? 'border-yellow-600 bg-yellow-900/20'
              : 'border-gray-700 bg-gray-800/50 hover:border-gray-600',
            dragIdx === idx ? 'opacity-40' : 'opacity-100'
          ]"
          draggable="true"
          @dragstart="onDragStart(idx)"
          @dragover="onDragOver($event, idx)"
          @dragend="onDragEnd"
        >
          <!-- 拖拽手柄 -->
          <div class="cursor-grab text-gray-600 group-hover:text-gray-400 pt-1.5 select-none">⠿⠿</div>

          <!-- 序号 -->
          <span class="shrink-0 w-6 h-6 rounded-full bg-gray-700 flex items-center justify-center text-xs font-mono text-gray-400 mt-1">
            {{ idx + 1 }}
          </span>

          <!-- 内容区 -->
          <div class="flex-1 grid grid-cols-1 sm:grid-cols-3 gap-2">
            <!-- 开始时间 -->
            <div>
              <label class="label">开始时间</label>
              <input
                v-model="item.startTime"
                type="time"
                class="input font-mono"
                :class="conflictIndices.has(idx) ? 'border-yellow-600' : ''"
                :disabled="disabled"
                @change="commit"
              />
            </div>

            <!-- 类型切换 -->
            <div>
              <label class="label">类型</label>
              <select
                v-model="item.itemType"
                class="input"
                :disabled="disabled"
                @change="() => { item.promoVideoId = undefined; item.streamSourceId = undefined; commit() }"
              >
                <option value="promo_video">🎬 宣传片循环</option>
                <option value="live_stream">📡 直播流</option>
              </select>
            </div>

            <!-- 源选择器 -->
            <div>
              <label class="label">
                {{ item.itemType === 'promo_video' ? '宣传片' : '直播源' }}
              </label>
              <!-- 宣传片 -->
              <select
                v-if="item.itemType === 'promo_video'"
                v-model="item.promoVideoId"
                class="input"
                :disabled="disabled"
                @change="commit"
              >
                <option :value="undefined" disabled>— 请选择 —</option>
                <option
                  v-for="v in readyVideos"
                  :key="v.id"
                  :value="v.id"
                >
                  {{ v.originalFilename }} {{ durationLabel(v) ? `(${durationLabel(v)})` : '' }}
                </option>
                <option v-if="readyVideos.length === 0" disabled>暂无已转码完成的宣传片</option>
              </select>
              <!-- 直播源 -->
              <select
                v-else
                v-model="item.streamSourceId"
                class="input"
                :disabled="disabled"
                @change="commit"
              >
                <option :value="undefined" disabled>— 请选择 —</option>
                <option
                  v-for="s in activeStreams"
                  :key="s.id"
                  :value="s.id"
                >
                  {{ s.name }}
                </option>
                <option v-if="activeStreams.length === 0" disabled>暂无可用直播源</option>
              </select>
            </div>
          </div>

          <!-- 删除按钮 -->
          <button
            class="shrink-0 mt-1 text-gray-600 hover:text-red-400 transition-colors text-lg leading-none"
            :disabled="disabled"
            title="删除"
            @click="removeItem(idx)"
          >✕</button>
        </div>
      </TransitionGroup>
    </div>

    <!-- 空状态 -->
    <div v-if="items.length === 0"
         class="rounded-xl border border-dashed border-gray-700 p-8 text-center text-gray-500 text-sm">
      暂无排期条目，点击下方"添加"按钮开始编排
    </div>

    <!-- 添加按钮 -->
    <button
      class="btn-ghost w-full border border-dashed border-gray-700 hover:border-gray-500"
      :disabled="disabled"
      @click="addItem"
    >
      + 添加条目
    </button>
  </div>
</template>

<style scoped>
.list-move, .list-enter-active, .list-leave-active { transition: all 0.2s ease; }
.list-enter-from, .list-leave-to { opacity: 0; transform: translateY(-8px); }
.list-leave-active { position: absolute; width: 100%; }
</style>
