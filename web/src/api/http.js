import axios from 'axios'
import { ElMessage } from 'element-plus'

const http = axios.create({
  baseURL: '/api',
  timeout: 30000
})

http.interceptors.request.use((config) => {
  const token = localStorage.getItem('relay_token')
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

http.interceptors.response.use(
  (response) => {
    const body = response.data
    // 管理 API 统一返回 { code, msg, data }，页面按内层 data 使用。
    if (body && typeof body === 'object' && Object.prototype.hasOwnProperty.call(body, 'code')) {
      if (body.code === 0) {
        return body.data
      }
      ElMessage.error(body.msg || '请求失败')
      return Promise.reject(new Error(body.msg || '请求失败'))
    }
    return body
  },
  (error) => {
    const status = error.response?.status
    const message = error.response?.data?.msg || error.message || '请求失败'
    if (status === 401) {
      localStorage.removeItem('relay_token')
      localStorage.removeItem('relay_user')
      if (!window.location.pathname.startsWith('/login')) {
        window.location.href = '/login'
      }
    }
    ElMessage.error(message)
    return Promise.reject(error)
  }
)

export default http
