import { createRouter, createWebHistory } from 'vue-router'

import Layout from '../layout/Layout.vue'
import LoginView from '../views/LoginView.vue'
import DashboardView from '../views/DashboardView.vue'
import ConsumersView from '../views/ConsumersView.vue'
import WecomConfigView from '../views/WecomConfigView.vue'
import PushRecordsView from '../views/PushRecordsView.vue'
import DeadLettersView from '../views/DeadLettersView.vue'
import AdminUsersView from '../views/AdminUsersView.vue'
import AuditLogsView from '../views/AuditLogsView.vue'

const routes = [
  { path: '/login', name: 'login', component: LoginView, meta: { public: true, title: '登录' } },
  {
    path: '/',
    component: Layout,
    redirect: '/dashboard',
    children: [
      { path: 'dashboard', name: 'dashboard', component: DashboardView, meta: { title: '概览' } },
      { path: 'consumers', name: 'consumers', component: ConsumersView, meta: { title: '消费端管理' } },
      { path: 'wecom-config', name: 'wecom-config', component: WecomConfigView, meta: { title: '企微参数' } },
      { path: 'push-records', name: 'push-records', component: PushRecordsView, meta: { title: '推送记录' } },
      { path: 'dead-letters', name: 'dead-letters', component: DeadLettersView, meta: { title: '死信管理' } },
      {
        path: 'admin-users',
        name: 'admin-users',
        component: AdminUsersView,
        meta: { title: '管理员', roles: ['admin'] }
      },
      { path: 'audit-logs', name: 'audit-logs', component: AuditLogsView, meta: { title: '操作审计' } }
    ]
  },
  { path: '/:pathMatch(.*)*', redirect: '/dashboard' }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

router.beforeEach((to) => {
  const token = localStorage.getItem('relay_token')
  if (!to.meta.public && !token) {
    return { path: '/login', query: { redirect: to.fullPath } }
  }
  if (to.path === '/login' && token) {
    return { path: '/dashboard' }
  }
  if (to.meta.roles) {
    let user = null
    try {
      user = JSON.parse(localStorage.getItem('relay_user') || 'null')
    } catch {
      user = null
    }
    if (!user || !to.meta.roles.includes(user.role)) {
      return { path: '/dashboard' }
    }
  }
  return true
})

router.afterEach((to) => {
  document.title = to.meta.title ? `${to.meta.title} - 企微回调转发` : '企微回调转发'
})

export default router
