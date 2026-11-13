<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h2 class="page-title">推送记录</h2>
        <p class="page-subtitle">按事件 × 消费端记录全部投递任务与最近一次投递结果</p>
      </div>
      <div>
        <el-button
          v-if="canWrite"
          type="danger"
          :disabled="selection.length === 0"
          :loading="batchDeleting"
          @click="handleBatchDelete"
        >
          批量删除（{{ selection.length }}）
        </el-button>
        <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
      </div>
    </div>

    <div class="panel">
      <div class="toolbar">
        <el-input
          v-model="query.keyword"
          placeholder="事件 ID / 消费端名称"
          clearable
          style="width: 220px"
          @keyup.enter="search"
          @clear="search"
        />
        <el-select v-model="query.status" placeholder="状态" clearable style="width: 130px" @change="search">
          <el-option label="待投递" value="pending" />
          <el-option label="投递中" value="sending" />
          <el-option label="成功" value="success" />
          <el-option label="失败" value="failed" />
          <el-option label="已转死信" value="dead" />
        </el-select>
        <el-select v-model="query.forward_mode" placeholder="转发模式" clearable style="width: 150px" @change="search">
          <el-option label="JSON 模式" value="json" />
          <el-option label="企微兼容模式" value="wecom_xml" />
          <el-option label="原样透传" value="wecom_proxy" />
        </el-select>
        <el-date-picker
          v-model="dateRange"
          type="daterange"
          value-format="YYYY-MM-DD"
          start-placeholder="开始日期"
          end-placeholder="结束日期"
          style="width: 260px"
          @change="search"
        />
        <el-button :icon="Search" @click="search">查询</el-button>
      </div>

      <el-table
        v-loading="loading"
        :data="rows"
        border
        stripe
        @selection-change="selection = $event"
      >
        <el-table-column
          v-if="canWrite"
          type="selection"
          width="46"
          :selectable="canDelete"
        />
        <el-table-column prop="event_id" label="事件 ID" min-width="190" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="mono">{{ row.event_id }}</span>
          </template>
        </el-table-column>
        <el-table-column label="消费端" min-width="130" show-overflow-tooltip>
          <template #default="{ row }">
            {{ row.consumer_name || `#${row.consumer_id}` }}
          </template>
        </el-table-column>
        <el-table-column label="模式" width="110" align="center">
          <template #default="{ row }">
            <el-tag v-if="row.forward_mode === 'wecom_xml'" type="warning" effect="light">企微兼容</el-tag>
            <el-tag v-else-if="row.forward_mode === 'wecom_proxy'" type="success" effect="light">原样透传</el-tag>
            <el-tag v-else type="primary" effect="light">JSON</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="100" align="center">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)" effect="light">{{ statusLabel(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="最近 HTTP" width="90" align="center">
          <template #default="{ row }">
            <el-tag
              v-if="row.last_http_status"
              :type="httpTagType(row.last_http_status)"
              effect="light"
            >
              {{ row.last_http_status }}
            </el-tag>
            <span v-else class="hint">-</span>
          </template>
        </el-table-column>
        <el-table-column label="耗时" width="90" align="center">
          <template #default="{ row }">{{ formatLatency(row.last_latency_ms) }}</template>
        </el-table-column>
        <el-table-column label="重试" width="80" align="center">
          <template #default="{ row }">{{ row.retry_count }}/{{ row.max_retry }}</template>
        </el-table-column>
        <el-table-column prop="last_error" label="最后错误" min-width="220" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="hint">{{ row.last_error || '-' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="最近投递" width="170" align="center">
          <template #default="{ row }">{{ formatTime(row.updated_at || row.created_at) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="120" fixed="right" align="center">
          <template #default="{ row }">
            <el-button link type="primary" @click="openDetail(row)">详情</el-button>
            <el-button
              v-if="canWrite"
              link
              type="danger"
              :disabled="!canDelete(row)"
              @click="handleDelete(row)"
            >
              删除
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="pagination">
        <el-pagination
          v-model:current-page="query.page"
          v-model:page-size="query.page_size"
          :total="total"
          :page-sizes="[10, 20, 50, 100]"
          layout="total, sizes, prev, pager, next"
          @size-change="load"
          @current-change="load"
        />
      </div>
    </div>

    <el-drawer v-model="detailVisible" title="推送记录详情" size="680px" destroy-on-close>
      <template v-if="detail">
        <el-descriptions :column="2" border>
          <el-descriptions-item label="任务 ID">{{ detail.id }}</el-descriptions-item>
          <el-descriptions-item label="消费端">
            {{ detail.consumer_name || `#${detail.consumer_id}` }}
          </el-descriptions-item>
          <el-descriptions-item label="事件 ID" :span="2">
            <span class="mono">{{ detail.event_id }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="消费端 ID">{{ detail.consumer_id }}</el-descriptions-item>
          <el-descriptions-item label="转发模式">
            {{
              detail.forward_mode === 'wecom_xml'
                ? '企微兼容模式'
                : detail.forward_mode === 'wecom_proxy'
                  ? '原样透传'
                  : 'JSON 模式'
            }}
          </el-descriptions-item>
          <el-descriptions-item label="AgentID">{{ detail.agent_id || '-' }}</el-descriptions-item>
          <el-descriptions-item label="状态">
            <el-tag :type="statusType(detail.status)">{{ statusLabel(detail.status) }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="重试次数">
            {{ detail.retry_count }} / {{ detail.max_retry }}
          </el-descriptions-item>
          <el-descriptions-item label="最近 HTTP">
            {{ detail.last_http_status || '-' }}
          </el-descriptions-item>
          <el-descriptions-item label="最近耗时">
            {{ formatLatency(detail.last_latency_ms) }}
          </el-descriptions-item>
          <el-descriptions-item label="下次重试" :span="2">
            {{ detail.next_retry_at ? formatTime(detail.next_retry_at) : '-' }}
          </el-descriptions-item>
          <el-descriptions-item label="回调地址" :span="2">
            <span class="mono">{{ detail.callback_url || '-' }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="创建时间">{{ formatTime(detail.created_at) }}</el-descriptions-item>
          <el-descriptions-item label="更新时间">{{ formatTime(detail.updated_at) }}</el-descriptions-item>
          <el-descriptions-item label="最后错误" :span="2">
            <span class="hint">{{ detail.last_error || '-' }}</span>
          </el-descriptions-item>
        </el-descriptions>

        <el-alert
          v-if="detail.status === 'dead'"
          type="warning"
          :closable="false"
          show-icon
          title="该记录已转死信"
          description="人工重推请前往死信管理；重推结果不会回写本页推送记录。"
          style="margin-top: 16px"
        />

        <h4 style="margin: 20px 0 8px">JSON 载荷</h4>
        <pre class="pre-block">{{ prettyJSON(detail.json_payload) }}</pre>

        <h4 style="margin: 20px 0 8px">原始明文 XML</h4>
        <pre class="pre-block">{{ detail.raw_xml || '（无）' }}</pre>

        <template v-if="detail.forward_mode === 'wecom_proxy'">
          <h4 style="margin: 20px 0 8px">原始加密报文（原样透传）</h4>
          <pre class="pre-block">{{ detail.raw_encrypted_body || '（无）' }}</pre>
          <h4 style="margin: 20px 0 8px">原始 Query</h4>
          <pre class="pre-block">{{ detail.inbound_query || '（无）' }}</pre>
        </template>

        <div v-if="canWrite" style="margin-top: 20px">
          <el-button
            v-if="canDelete(detail)"
            type="danger"
            plain
            @click="handleDelete(detail, true)"
          >
            永久删除
          </el-button>
          <p v-else class="hint">待投递/投递中的任务不可删除，请等待投递结束。</p>
        </div>
        <p v-else class="hint" style="margin-top: 16px">当前为只读账号，无法删除推送记录。</p>
      </template>
    </el-drawer>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh, Search } from '@element-plus/icons-vue'
import {
  batchDeletePushRecords,
  deletePushRecord,
  getPushRecord,
  listPushRecords
} from '../api'
import { useAuthStore } from '../store/auth'

const auth = useAuthStore()
const canWrite = computed(() => auth.isAdmin)
const loading = ref(false)
const rows = ref([])
const total = ref(0)
const selection = ref([])
const batchDeleting = ref(false)
const dateRange = ref([])
const query = reactive({ page: 1, page_size: 20, keyword: '', status: '', forward_mode: '' })

const detailVisible = ref(false)
const detail = ref(null)

const statusLabels = {
  pending: '待投递',
  sending: '投递中',
  success: '成功',
  failed: '失败',
  dead: '已转死信'
}
const statusTypes = {
  pending: 'info',
  sending: 'warning',
  success: 'success',
  failed: 'danger',
  dead: 'danger'
}

function statusLabel(status) {
  return statusLabels[status] || status
}
function statusType(status) {
  return statusTypes[status] || 'info'
}
function httpTagType(code) {
  if (code >= 200 && code < 300) return 'success'
  if (code >= 300 && code < 400) return 'warning'
  return 'danger'
}
function canDelete(row) {
  return ['success', 'failed', 'dead'].includes(row.status)
}
function formatTime(value) {
  return value ? new Date(value).toLocaleString() : '-'
}
function formatLatency(ms) {
  if (!ms || ms <= 0) return '-'
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(2)} s`
}
function prettyJSON(raw) {
  if (!raw) return '（无）'
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}

async function load() {
  loading.value = true
  try {
    const params = {
      page: query.page,
      page_size: query.page_size,
      keyword: query.keyword || undefined,
      status: query.status || undefined,
      forward_mode: query.forward_mode || undefined
    }
    if (dateRange.value?.length === 2) {
      params.start_time = dateRange.value[0]
      params.end_time = dateRange.value[1]
    }
    const data = await listPushRecords(params)
    rows.value = data.list
    total.value = data.total
  } finally {
    loading.value = false
  }
}

function search() {
  query.page = 1
  load()
}

async function openDetail(row) {
  const data = await getPushRecord(row.id)
  detail.value = data
  detailVisible.value = true
}

async function handleDelete(row, fromDrawer = false) {
  try {
    await ElMessageBox.confirm(
      `确认永久删除推送记录「${row.event_id}」？删除后不可恢复，且会影响概览统计中的任务计数。若任务已转死信，死信记录仍会保留。`,
      '删除确认',
      { type: 'warning', confirmButtonText: '永久删除', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  await deletePushRecord(row.id)
  ElMessage.success('已删除')
  if (fromDrawer || detail.value?.id === row.id) {
    detailVisible.value = false
  }
  await load()
}

async function handleBatchDelete() {
  const ids = selection.value.map((item) => item.id)
  if (ids.length === 0) {
    ElMessage.warning('请选择要删除的推送记录')
    return
  }
  if (ids.length > 100) {
    ElMessage.warning('单次最多删除 100 条')
    return
  }
  try {
    await ElMessageBox.confirm(
      `确认永久删除选中的 ${ids.length} 条推送记录？删除后不可恢复，且会影响概览统计中的任务计数。`,
      '批量删除确认',
      { type: 'warning', confirmButtonText: '永久删除', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  batchDeleting.value = true
  try {
    const result = await batchDeletePushRecords(ids)
    ElMessage.success(`删除完成：删除 ${result.deleted} 条，跳过 ${result.skipped} 条`)
    if (result.skipped > 0) {
      ElMessage.warning('仅已结束（成功/失败/已转死信）的任务可以删除，待投递/投递中的任务已跳过')
    }
    await load()
  } finally {
    batchDeleting.value = false
  }
}

onMounted(load)
</script>
