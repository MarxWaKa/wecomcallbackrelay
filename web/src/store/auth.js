import { defineStore } from 'pinia'
import { getUserInfo } from '../api'

const TOKEN_KEY = 'relay_token'
const USER_KEY = 'relay_user'

function readUser() {
  try {
    return JSON.parse(localStorage.getItem(USER_KEY) || 'null')
  } catch {
    return null
  }
}

export const useAuthStore = defineStore('auth', {
  state: () => ({
    token: localStorage.getItem(TOKEN_KEY) || '',
    user: readUser()
  }),
  getters: {
    isAdmin: (state) => state.user?.role === 'admin'
  },
  actions: {
    setLogin(payload) {
      if (!payload?.token) {
        throw new Error('登录响应缺少令牌')
      }
      this.token = payload.token
      this.user = payload.user
      localStorage.setItem(TOKEN_KEY, payload.token)
      localStorage.setItem(USER_KEY, JSON.stringify(payload.user))
    },
    setUser(user) {
      this.user = user
      localStorage.setItem(USER_KEY, JSON.stringify(user))
    },
    clear() {
      this.token = ''
      this.user = null
      localStorage.removeItem(TOKEN_KEY)
      localStorage.removeItem(USER_KEY)
    },
    async refreshUser() {
      const data = await getUserInfo()
      this.setUser(data)
      return data
    }
  }
})
