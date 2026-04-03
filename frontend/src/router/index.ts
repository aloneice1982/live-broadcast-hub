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
      return auth.user?.role === 'super_admin' ? '/dashboard' : `/city/${auth.user?.cityId}`
    }}
  ]
})

router.beforeEach((to) => {
  const auth = useAuthStore()
  if (!to.meta.public && !auth.token) return '/login'
  if (to.meta.role && auth.user?.role !== to.meta.role) {
    return auth.user?.role === 'super_admin' ? '/dashboard' : `/city/${auth.user?.cityId}`
  }
})

export default router
