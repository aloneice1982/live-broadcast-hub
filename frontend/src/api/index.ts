import axios from 'axios'
import router from '@/router'

export const api = axios.create({ baseURL: '/api' })

// 401 自动跳登录页（清除本地状态 + 提示用户）
// 注意：跳过登录接口本身，避免密码错误时误弹"登录已失效"
api.interceptors.response.use(
  res => res,
  err => {
    if (err.response?.status === 401 && !err.config?.url?.includes('/auth/login')) {
      localStorage.removeItem('token')
      localStorage.removeItem('user')
      alert('登录已失效，请重新登录')
      router.push('/login')
    }
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
    ),
  setMute: (cityId: number, muted: boolean) =>
    api.post(`/cities/${cityId}/ffmpeg/mute`, { muted }),
  insertPromo: (cityId: number, promoVideoId: number, loop: boolean) =>
    api.post<{ data: { inserting: boolean } }>(
      `/cities/${cityId}/ffmpeg/insert-promo`, { promoVideoId, loop }
    ),
  stopPromo: (cityId: number) =>
    api.post(`/cities/${cityId}/ffmpeg/stop-promo`),
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
  create: (data: any) => api.post('/users', data),
  remove: (userId: number) => api.delete(`/users/${userId}`),
  changePassword: (userId: number, password: string) =>
    api.put(`/users/${userId}/password`, { password }),
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
    `/api/cities/${cityId}/videos/${videoId}/thumbnail?t=${videoId}`,
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
  progressPct: number
}

export interface StreamConfig {
  id: number; cityId: number; pushUrl?: string; pushKey?: string
  volumeGain: number; srsApp: string; srsStream: string
  configLocked: boolean
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
  promoInserting?: boolean // 是否正在插播宣传片
  promoLoop?: boolean      // 是否循环播放
  promoRemainingSecs?: number // 剩余秒数（循环模式为本轮剩余）
  promoStartedAt?: string  // ISO8601 UTC 时间戳，前端用于本地实时倒计时
  promoVideoDuration?: number // 宣传片时长（秒），前端倒计时用
}

export interface User {
  id: number; username: string; role: 'super_admin' | 'city_admin' | 'observer'; cityId?: number; phone?: string
}

export interface AlertLog {
  id: number; cityId: number; level: 'warn' | 'error' | 'critical'
  message: string; smsSent: boolean; createdAt: string
}
