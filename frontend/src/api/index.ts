import axios from 'axios'
import router from '@/router'

export const api = axios.create({ baseURL: '/api' })

// 401 自动跳登录页
api.interceptors.response.use(
  res => res,
  err => {
    if (err.response?.status === 401) router.push('/login')
    return Promise.reject(err)
  }
)

// ── Auth ──────────────────────────────────────────────────────
export const authAPI = {
  login: (username: string, password: string) =>
    api.post<{ data: { token: string; user: any } }>('/auth/login', { username, password })
}

// ── Cities ────────────────────────────────────────────────────
export const citiesAPI = {
  list: () => api.get<{ data: City[] }>('/cities'),
  getStatus: (cityId: number) => api.get<{ data: ProcessStatus }>(`/cities/${cityId}/status`),
  resetFFmpeg: (cityId: number) => api.post(`/cities/${cityId}/ffmpeg/reset`),
  directPush: (cityId: number, streamSourceId: number) =>
    api.post<{ data: { started: boolean; sourceName: string } }>(
      `/cities/${cityId}/ffmpeg/direct-push`, { streamSourceId }
    )
}

// ── Stream Sources ────────────────────────────────────────────
export const streamSourcesAPI = {
  list: () => api.get<{ data: StreamSource[] }>('/stream-sources'),
  create: (data: Partial<StreamSource> & { name: string; url: string }) => api.post('/stream-sources', data),
  update: (id: number, data: Partial<StreamSource>) => api.put(`/stream-sources/${id}`, data),
  remove: (id: number) => api.delete(`/stream-sources/${id}`),
  downloadTemplate: () => api.get('/stream-sources/template', { responseType: 'blob' }),
  importCSV: (file: File) => {
    const fd = new FormData()
    fd.append('file', file)
    return api.post<{ data: { imported: number; skipped: number } }>(
      '/stream-sources/import', fd,
      { headers: { 'Content-Type': 'multipart/form-data' } }
    )
  }
}

// ── Users ─────────────────────────────────────────────────────
export const usersAPI = {
  list: () => api.get<{ data: User[] }>('/users'),
  create: (data: any) => api.post('/users', data)
}

// ── Videos ───────────────────────────────────────────────────
export const videosAPI = {
  list: (cityId: number) => api.get<{ data: PromoVideo[] }>(`/cities/${cityId}/videos`),
  upload: (cityId: number, file: File, onProgress?: (pct: number) => void, signal?: AbortSignal) => {
    const fd = new FormData()
    fd.append('file', file)
    return api.post<{ data: { id: number } }>(`/cities/${cityId}/videos/upload`, fd, {
      headers: { 'Content-Type': 'multipart/form-data' },
      onUploadProgress: e => onProgress?.(Math.round((e.loaded * 100) / (e.total ?? 1))),
      signal
    })
  },
  rename: (cityId: number, videoId: number, displayName: string) =>
    api.put(`/cities/${cityId}/videos/${videoId}`, { displayName }),
  thumbnailUrl: (cityId: number, videoId: number) =>
    `/api/cities/${cityId}/videos/${videoId}/thumbnail`,
  remove: (cityId: number, videoId: number) => api.delete(`/cities/${cityId}/videos/${videoId}`)
}

// ── Stream Config ─────────────────────────────────────────────
export const streamConfigAPI = {
  get: (cityId: number) => api.get<{ data: StreamConfig }>(`/cities/${cityId}/stream-config`),
  update: (cityId: number, data: Partial<StreamConfig>) =>
    api.put(`/cities/${cityId}/stream-config`, data)
}

// ── Schedules ─────────────────────────────────────────────────
export const schedulesAPI = {
  list: (cityId: number, date?: string) =>
    api.get<{ data: Schedule[] }>(`/cities/${cityId}/schedules`, { params: { date } }),
  create: (cityId: number, date: string) => api.post<{ data: { id: number } }>(`/cities/${cityId}/schedules`, { date }),
  updateItems: (cityId: number, scheduleId: number, items: ScheduleItemReq[]) =>
    api.put(`/cities/${cityId}/schedules/${scheduleId}/items`, items),
  start: (cityId: number, scheduleId: number, manual = false, forceNow = false) =>
    api.post(`/cities/${cityId}/schedules/${scheduleId}/start`, { manual, forceNow }),
  stop: (cityId: number, scheduleId: number) =>
    api.post(`/cities/${cityId}/schedules/${scheduleId}/stop`),
  advance: (cityId: number, scheduleId: number) =>
    api.post(`/cities/${cityId}/schedules/${scheduleId}/advance`),
  delete: (cityId: number, scheduleId: number) =>
    api.delete(`/cities/${cityId}/schedules/${scheduleId}`)
}

// ── Alerts ────────────────────────────────────────────────────
export const alertsAPI = {
  list: (cityId: number) => api.get<{ data: AlertLog[] }>(`/cities/${cityId}/alerts`),
  clear: (cityId: number) => api.post(`/cities/${cityId}/alerts/clear`)
}

// ── System ────────────────────────────────────────────────────
export const systemAPI = {
  metrics: () => api.get<{ data: { uploadMbps: number; downloadMbps: number } }>('/system/metrics')
}

// ── 类型定义 ──────────────────────────────────────────────────

export interface City { id: number; name: string; code: string }

export interface StreamSource {
  id: number; name: string; url: string
  matchDatetime?: string; round?: string; channel?: string; remark?: string
  isActive: boolean
}

export interface PromoVideo {
  id: number; cityId: number; originalFilename: string; storedFilename: string
  displayName?: string; hasThumbnail: boolean
  transcodeStatus: 'pending' | 'processing' | 'done' | 'failed'
  transcodeError?: string; durationSeconds?: number; createdAt: string
}

export interface StreamConfig {
  id: number; cityId: number; pushUrl?: string; pushKey?: string
  volumeGain: number; srsApp: string; srsStream: string
}

export interface Schedule {
  id: number; cityId: number; date: string
  status: 'draft' | 'running' | 'stopped' | 'finished'
  items?: ScheduleItem[]
}

export interface ScheduleItem {
  id: number; scheduleId: number; orderIndex: number
  itemType: 'promo_video' | 'live_stream'
  promoVideoId?: number; loopCount: number
  streamSourceId?: number; startTime: string
}

export interface ScheduleItemReq {
  orderIndex: number; itemType: 'promo_video' | 'live_stream'
  promoVideoId?: number; loopCount?: number
  streamSourceId?: number; startTime: string
}

export interface ProcessStatus {
  id: number; cityId: number; scheduleId?: number
  status: 'idle' | 'warming' | 'streaming' | 'failed' | 'breaker_open'
  currentItemId?: number; retryCount: number
  currentItemName?: string
  todayItemCount: number
  schedulerActive: boolean
  scheduleStatus?: string
  currentItemIndex: number
  nextItemName?: string
  nextItemTime?: string
  lastStartedAt?: string   // 东八区本地时间字符串 "YYYY-MM-DD HH:MM:SS"
}

export interface User {
  id: number; username: string; role: string; cityId?: number; phone?: string
}

export interface AlertLog {
  id: number; cityId: number; level: 'warn' | 'error' | 'critical'
  message: string; smsSent: boolean; createdAt: string
}
