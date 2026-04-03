<script setup lang="ts">
import { ref } from 'vue'
import { videosAPI, type PromoVideo } from '@/api'

const props = defineProps<{ cityId: number }>()
const emit = defineEmits<{ (e: 'uploaded', video: PromoVideo): void }>()

const dragging = ref(false)
const uploading = ref(false)
const progress = ref(0)
const error = ref('')
let abortController: AbortController | null = null

function onDragOver(e: DragEvent) { e.preventDefault(); dragging.value = true }
function onDragLeave() { dragging.value = false }
function onDrop(e: DragEvent) {
  e.preventDefault()
  dragging.value = false
  const file = e.dataTransfer?.files[0]
  if (file) doUpload(file)
}
function onFileChange(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0]
  if (file) doUpload(file)
  // 重置 input 值，允许重新选同一个文件
  ;(e.target as HTMLInputElement).value = ''
}

async function doUpload(file: File) {
  if (!file.name.toLowerCase().endsWith('.mp4')) {
    error.value = '仅支持 .mp4 格式'; return
  }
  error.value = ''
  uploading.value = true
  progress.value = 0
  abortController = new AbortController()
  try {
    const res = await videosAPI.upload(props.cityId, file, pct => { progress.value = pct }, abortController.signal)
    emit('uploaded', res.data.data as any)
  } catch (e: any) {
    if (e.code === 'ERR_CANCELED' || e.name === 'CanceledError') {
      error.value = '已取消上传'
    } else {
      error.value = e.response?.data?.error ?? '上传失败'
    }
  } finally {
    uploading.value = false
    progress.value = 0
    abortController = null
  }
}

function cancelUpload() {
  abortController?.abort()
}
</script>

<template>
  <div>
    <!-- 拖拽上传区域 -->
    <div
      class="relative border-2 border-dashed rounded-xl p-8 text-center transition-colors"
      :class="dragging ? 'border-blue-500 bg-blue-950/30' : 'border-gray-700 hover:border-gray-600'"
      @dragover="onDragOver"
      @dragleave="onDragLeave"
      @drop="onDrop"
    >
      <input
        v-if="!uploading"
        type="file" accept=".mp4"
        class="absolute inset-0 opacity-0 cursor-pointer"
        @change="onFileChange"
      />

      <div v-if="!uploading" class="space-y-2 pointer-events-none">
        <div class="text-4xl">🎬</div>
        <p class="text-sm font-medium text-gray-300">点击或拖拽上传宣传片</p>
        <p class="text-xs text-gray-500">仅支持 .mp4，上传后自动进行离线标准化转码</p>
      </div>

      <!-- 上传进度 -->
      <div v-else class="space-y-3">
        <p class="text-sm text-blue-400 font-medium">上传中… {{ progress }}%</p>
        <div class="h-2 bg-gray-800 rounded-full overflow-hidden">
          <div
            class="h-full bg-blue-500 rounded-full transition-all duration-300"
            :style="{ width: progress + '%' }"
          />
        </div>
        <button
          class="btn-ghost text-xs py-1 px-3 pointer-events-auto"
          @click.stop="cancelUpload"
        >取消上传</button>
      </div>
    </div>

    <!-- 错误提示 -->
    <p v-if="error" class="mt-2 text-xs text-red-400">⚠ {{ error }}</p>
  </div>
</template>
