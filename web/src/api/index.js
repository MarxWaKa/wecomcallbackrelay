import http from './http'

// 登录与账号
export const login = (data) => http.post('/base/login', data)
export const getUserInfo = () => http.get('/base/user-info')
export const logout = () => http.post('/base/logout')
export const changePassword = (data) => http.post('/base/change-password', data)

// 概览
export const getDashboardSummary = () => http.get('/dashboard/summary')

// 消费端
export const listConsumers = (params) => http.get('/consumers', { params })
export const getConsumer = (id) => http.get(`/consumers/${id}`)
export const createConsumer = (data) => http.post('/consumers', data)
export const updateConsumer = (id, data) => http.put(`/consumers/${id}`, data)
export const deleteConsumer = (id) => http.delete(`/consumers/${id}`)
export const testConsumer = (id) => http.post(`/consumers/${id}/test`)

// 企微参数
export const listWecomConfigs = () => http.get('/wecom-config')
export const createWecomConfig = (data) => http.post('/wecom-config', data)
export const updateWecomConfig = (id, data) => http.put(`/wecom-config/${id}`, data)
export const deleteWecomConfig = (id) => http.delete(`/wecom-config/${id}`)

// 推送记录
export const listPushRecords = (params) => http.get('/push-records', { params })
export const getPushRecord = (id) => http.get(`/push-records/${id}`)
export const deletePushRecord = (id) => http.delete(`/push-records/${id}`)
export const batchDeletePushRecords = (ids) => http.post('/push-records/batch-delete', { ids })

// 死信
export const listDeadLetters = (params) => http.get('/dead-letters', { params })
export const getDeadLetter = (id) => http.get(`/dead-letters/${id}`)
export const replayDeadLetter = (id, useSnapshot = false) =>
  http.post(`/dead-letters/${id}/replay`, null, {
    params: { use_snapshot: useSnapshot },
    timeout: 180000
  })
export const batchReplayDeadLetters = (ids, useSnapshot = false) =>
  http.post('/dead-letters/replay-batch', { ids, use_snapshot: useSnapshot }, { timeout: 180000 })
export const discardDeadLetter = (id) => http.put(`/dead-letters/${id}/discard`)
export const deleteDeadLetter = (id) => http.delete(`/dead-letters/${id}`)
export const batchDeleteDeadLetters = (ids) => http.post('/dead-letters/batch-delete', { ids })

// 管理员
export const listAdminUsers = (params) => http.get('/admin-users', { params })
export const createAdminUser = (data) => http.post('/admin-users', data)
export const updateAdminUser = (id, data) => http.put(`/admin-users/${id}`, data)
export const deleteAdminUser = (id) => http.delete(`/admin-users/${id}`)
export const resetAdminPassword = (id, data) => http.put(`/admin-users/${id}/reset-password`, data)

// 审计日志
export const listAuditLogs = (params) => http.get('/audit-logs', { params })
