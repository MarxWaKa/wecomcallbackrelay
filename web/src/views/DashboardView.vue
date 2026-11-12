<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h2 class="page-title">运行概览</h2>
        <p class="page-subtitle">企微回调接收、异步转发与死信状态一览</p>
      </div>
      <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
    </div>

    <el-row :gutter="14">
      <el-col :xs="24" :sm="12" :md="6">
        <div class="stat-card">
          <div class="label">启用消费端</div>
          <div class="value">{{ summary.consumers.enabled }}</div>
          <div class="foot">共 {{ summary.consumers.total }} 个消费端</div>
        </div>
      </el-col>
      <el-col :xs="24" :sm="12" :md="6">
        <div class="stat-card">
          <div class="label">待投递任务</div>
          <div class="value">{{ summary.tasks.pending }}</div>
          <div class="foot">Worker 每 {{ workerHint }} 轮询一次</div>
        </div>
      </el-col>
      <el-col :xs="24" :sm="12" :md="6">
        <div class="stat-card">
          <div class="label">今日投递成功</div>
          <div class="value">{{ summary.tasks.success_today }}</div>
          <div class="foot">今日死信 {{ summary.tasks.dead_today }}</div>
        </div>
      </el-col>
      <el-col :xs="24" :sm="12" :md="6">
        <div class="stat-card">
          <div class="label">待处理死信</div>
          <div class="value" :style="{ color: summary.dead_letters.pending > 0 ? '#dc2626' : undefined }">
            {{ summary.dead_letters.pending }}
          </div>
          <div class="foot">重推中 {{ summary.dead_letters.replaying }}</div>
        </div>
      </el-col>
    </el-row>

    <el-row :gutter="14" style="margin-top: 16px">
      <el-col :xs="24" :md="14">
        <div class="panel" style="height: 100%">
          <h3 style="margin-top: 0">接入状态</h3>
          <el-alert
            v-if="!wecomConfigured"
            type="warning"
            :closable="false"
            title="尚未配置企微参数"
            description="请前往“企微参数”填写 CorpID / Secret / Token / EncodingAESKey，然后在企微后台把通讯录同步回调 URL 配置为 https://你的域名/wecom/callback。"
            show-icon
          />
          <el-alert
            v-else
            type="success"
            :closable="false"
            title="企微参数已配置"
            description="回调入口：/wecom/callback。请确认企微后台的 Token 与 EncodingAESKey 与本服务一致。"
            show-icon
          />

          <el-descriptions :column="1" border style="margin-top: 16px">
            <el-descriptions-item label="回调地址">/wecom/callback</el-descriptions-item>
            <el-descriptions-item label="健康检查">/healthz</el-descriptions-item>
            <el-descriptions-item label="服务端时间">{{ serverTime }}</el-descriptions-item>
          </el-descriptions>
        </div>
      </el-col>
      <el-col :xs="24" :md="10">
        <div class="panel" style="height: 100%">
          <h3 style="margin-top: 0">快速开始</h3>
          <el-steps direction="vertical" :active="stepActive" finish-status="success">
            <el-step title="配置企微参数" description="填写 Secret / Token / EncodingAESKey / CorpID" />
            <el-step title="添加消费端" description="JSON 模式或企微兼容模式（存量系统零改造）" />
            <el-step title="切换企微回调 URL" description="低峰期在企微后台保存 https://域名/wecom/callback" />
            <el-step title="验证投递" description="在消费端列表点击“测试”或触发一次通讯录变更" />
          </el-steps>
        </div>
      </el-col>
    </el-row>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { getDashboardSummary, listWecomConfigs } from '../api'

const loading = ref(false)
const wecomConfigured = ref(false)
const workerHint = '2 秒'
const serverTime = ref('-')
const summary = reactive({
  consumers: { total: 0, enabled: 0 },
  tasks: { pending: 0, success_today: 0, dead_today: 0 },
  dead_letters: { pending: 0, replaying: 0 },
  quick_start: { has_inbound: false, has_success: false }
})

// el-steps 的 active 表示当前进行到第几步（0-based）：
// active=4 时 4 个步骤都已完成并显示对号。
const stepActive = computed(() => {
  if (!wecomConfigured.value) return 0
  if (summary.consumers.enabled === 0) return 1
  if (!summary.quick_start.has_inbound) return 2
  if (!summary.quick_start.has_success) return 3
  return 4
})

async function load() {
  loading.value = true
  try {
    const data = await getDashboardSummary()
    Object.assign(summary, {
      consumers: data.consumers,
      tasks: data.tasks,
      dead_letters: data.dead_letters,
      quick_start: data.quick_start || { has_inbound: false, has_success: false }
    })
    serverTime.value = new Date(data.server_time).toLocaleString()
    const configs = await listWecomConfigs()
    wecomConfigured.value = (configs.list || []).length > 0
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>
