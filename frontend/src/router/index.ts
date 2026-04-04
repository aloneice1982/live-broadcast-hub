import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', component: () => import('@/views/Login.vue'), meta: { public: true } },
    { path: '/dashboard', component: () => import('@/views/Dashboard.vue'), meta: { role: 'super_admin' } },
    { path: '/city/:cityId', component: () => import('@/views/CityConsole.vue') },
    { path: '/', redirect: () => {
      const auth = useAuthStore()
      if (!auth.token) return '/login'
      if (auth.user?.role === 'city_admin') return `/city/${auth.user.cityId}`
      return '/dashboard'  // super_admin + observer 都去大盘
    }}
  ]
})

router.beforeEach((to) => {
  const auth = useAuthStore()
  if (!to.meta.public && !auth.token) return '/login'
  if (to.meta.role === 'super_admin') {
    if (auth.user?.role !== 'super_admin' && auth.user?.role !== 'observer') {
      // 容错：city_admin 无 cityId 时回登录页，避免 /city/undefined 死循环
      return (auth.user?.role === 'city_admin' && auth.user?.cityId)
        ? `/city/${auth.user.cityId}`
        : '/login'
    }
  }
})

export default router
