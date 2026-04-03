<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { authAPI } from '@/api'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const auth = useAuthStore()

const form = ref({ username: '', password: '' })
const loading = ref(false)
const error = ref('')

async function submit() {
  if (!form.value.username || !form.value.password) {
    error.value = '请填写用户名和密码'; return
  }
  loading.value = true
  error.value = ''
  try {
    const res = await authAPI.login(form.value.username, form.value.password)
    const { token, user } = res.data.data
    auth.setAuth(token, user)
    router.push(user.role === 'super_admin' ? '/dashboard' : `/city/${user.cityId}`)
  } catch (e: any) {
    error.value = e.response?.data?.error ?? '登录失败，请检查用户名和密码'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="min-h-screen flex items-center justify-center p-4 bg-gray-950">
    <!-- 背景装饰 -->
    <div class="absolute inset-0 overflow-hidden pointer-events-none">
      <div class="absolute -top-40 -right-40 w-96 h-96 bg-blue-900/20 rounded-full blur-3xl" />
      <div class="absolute -bottom-40 -left-40 w-96 h-96 bg-indigo-900/20 rounded-full blur-3xl" />
    </div>

    <div class="relative w-full max-w-md">
      <!-- Logo 区域 -->
      <div class="text-center mb-8">
        <div class="text-5xl mb-3">📡</div>
        <h1 class="text-2xl font-bold text-white">苏超联赛</h1>
        <p class="text-gray-400 text-sm mt-1">全省直播分发中台</p>
      </div>

      <!-- 登录卡片 -->
      <div class="card shadow-2xl">
        <h2 class="text-base font-semibold text-gray-200 mb-5">管理员登录</h2>

        <form @submit.prevent="submit" class="space-y-4">
          <div>
            <label class="label">用户名</label>
            <input
              v-model="form.username"
              type="text"
              class="input"
              placeholder="admin"
              autocomplete="username"
              autofocus
            />
          </div>
          <div>
            <label class="label">密码</label>
            <input
              v-model="form.password"
              type="password"
              class="input"
              placeholder="••••••••"
              autocomplete="current-password"
            />
          </div>

          <!-- 错误提示 -->
          <div v-if="error"
               class="rounded-lg bg-red-900/40 border border-red-700 px-3 py-2 text-sm text-red-300">
            {{ error }}
          </div>

          <button
            type="submit"
            class="btn-primary w-full justify-center py-2.5"
            :disabled="loading"
          >
            <span v-if="loading" class="animate-spin">⏳</span>
            {{ loading ? '登录中…' : '登录' }}
          </button>
        </form>
      </div>
    </div>
  </div>
</template>
