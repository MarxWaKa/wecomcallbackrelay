<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h2 class="page-title">死信管理</h2>
        <p class="page-subtitle">自动重试耗尽的投递记录，可查看原始载荷并人工重推、标记丢弃或永久删除</p>
      </div>
      <div>
        <el-checkbox v-if="canWrite" v-model="pageUseSnapshot" style="margin-right: 12px">
          重推使用死信快照
        </el-checkbox>
        <el-button
          v-if="canWrite"
          type="warning"
          :disabled="replayableSelection.length === 0"
          :loading="batchReplaying"
          @click="handleBatchReplay"
        >
          批量重推（{{ replayableSelection.length }}）
        </el-button>
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
          <el-option label="待处理" value="pending" />
          <el-option label="重推中" value="replaying" />
          <el-option label="已解决" value="resolved" />
          <el-option label="已丢弃" value="discarded" />
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
        <el-table-column type="selection" width="46" :selectable="(row) => row.status !== 'replaying'" />
        <el-table-column prop="event_id" label="事件 ID" min-width="190" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="mono">{{ row.event_id }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="consumer_name" label="消费端" min-width="130" show-overflow-tooltip />
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
        <el-table-column prop="retry_count" label="重试" width="70" align="center" />
        <el-table-column prop="last_error" label="最后错误" min-width="220" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="hint">{{ row.last_error || '-' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="创建时间" width="170" align="center">
          <template #default="{ row }">{{ formatTime(row.created_at) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="270" fixed="right" align="center">
          <template #default="{ row }">
            <el-button link type="primary" @click="openDetail(row)">详情</el-button>
            <template v-if="canWrite">
              <el-button link type="success" :disabled="!canReplay(row)" @click="handleReplay(row)">重推</el-button>
              <el-button link type="warning" :disabled="row.status !== 'pending'" @click="handleDiscard(row)">
                丢弃
              </el-button>
              <el-button link type="danger" :disabled="row.status === 'replaying'" @click="handleDelete(row)">
                删除
              </el-button>
            </template>
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

    <el-drawer v-model="detailVisible" title="死信详情" size="620px" destroy-on-close>
      <template v-if="detail">
        <el-descriptions :column="2" border>
          <el-descriptions-item label="事件 ID" :span="2">
            <span class="mono">{{ detail.event_id }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="消费端">{{ detail.consumer_name }}</el-descriptions-item>
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
          <el-descriptions-item label="认证方式">{{ detail.auth_type || '-' }}</el-descriptions-item>
          <el-descriptions-item label="状态">
            <el-tag :type="statusType(detail.status)">{{ statusLabel(detail.status) }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="重试次数">{{ detail.retry_count }}</el-descriptions-item>
          <el-descriptions-item label="当前消费端状态">
            <el-tag :type="consumerStateType(detail)">{{ consumerStateLabel(detail) }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="当前转发模式">
            {{ forwardModeLabel(detail.current_forward_mode) }}
          </el-descriptions-item>
          <el-descriptions-item label="当前回调地址" :span="2">
            <span class="mono">{{ detail.current_callback_url || '-' }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="快照回调地址" :span="2">
            <span class="mono">{{ detail.callback_url }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="快照 CorpID" :span="2">
            <span class="mono">{{ detail.wecom_corp_id || '-' }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="快照 AgentID" :span="2">
            <span class="mono">{{ detail.wecom_agent_id || '-' }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="快照 Token">{{ detail.wecom_token_masked || '-' }}</el-descriptions-item>
          <el-descriptions-item label="快照 AESKey">
            {{ detail.wecom_encoding_aes_key_masked || '-' }}
          </el-descriptions-item>
          <el-descriptions-item label="快照密钥指纹" :span="2">
            <span class="mono">{{ detail.wecom_key_fingerprint || '-' }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="创建时间" :span="2">{{ formatTime(detail.created_at) }}</el-descriptions-item>
          <el-descriptions-item label="最后错误" :span="2">
            <span class="hint">{{ detail.last_error || '-' }}</span>
          </el-descriptions-item>
        </el-descriptions>

        <el-alert
          :type="consumerAvailable(detail) ? 'info' : 'warning'"
          :closable="false"
          show-icon
          :title="consumerAvailable(detail) ? '重推默认使用消费端最新配置' : '消费端不可用，需使用死信快照重推'"
          :description="
            consumerAvailable(detail)
              ? 'URL/鉴权/密钥/超时使用消费端当前配置；转发模式与载荷仍使用死信创建时的快照。勾选“使用死信快照配置”可复现历史配置。'
              : '消费端已删除或禁用，默认重推会被拒绝；请先恢复消费端，或勾选“使用死信快照配置”按历史快照重推。'
          "
          style="margin-top: 16px"
        />

        <h4 style="margin: 20px 0 8px">JSON 载荷</h4>
        <pre class="pre-block">{{ prettyJSON(detail.json_payload) }}</pre>

        <h4 style="margin: 20px 0 8px">原始明文 XML</h4>
        <pre class="pre-block">{{ detail.raw_xml || '（JSON 模式无原始 XML 快照）' }}</pre>

        <template v-if="detail.forward_mode === 'wecom_proxy'">
          <h4 style="margin: 20px 0 8px">原始加密报文（原样透传）</h4>
          <pre class="pre-block">{{ detail.raw_encrypted_body || '（无）' }}</pre>
          <h4 style="margin: 20px 0 8px">原始 Query</h4>
          <pre class="pre-block">{{ detail.inbound_query || '（无）' }}</pre>
        </template>

        <div v-if="canWrite" style="margin-top: 20px">
          <el-checkbox v-model="detailUseSnapshot" style="margin-bottom: 10px">
            使用死信快照配置
          </el-checkbox>
          <div style="display: flex; gap: 10px">
            <el-button
              type="success"
              :disabled="!canReplay(detail)"
              :loading="detailReplaying"
              @click="handleReplay(detail, true)"
            >
              立即重推
            </el-button>
            <el-button type="warning" plain :disabled="detail.status !== 'pending'" @click="handleDiscard(detail, true)">
              标记不再重推
            </el-button>
            <el-button type="danger" plain :disabled="detail.status === 'replaying'" @click="handleDelete(detail)">
              永久删除
            </el-button>
          </div>
        </div>
        <p v-else class="hint" style="margin-top: 16px">当前为只读账号，无法重推、丢弃或删除死信。</p>
      </template>
    </el-drawer>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh, Search } from '@element-plus/icons-vue'
import {
  batchDeleteDeadLetters,
  batchReplayDeadLetters,
  deleteDeadLetter,
  discardDeadLetter,
  getDeadLetter,
  listDeadLetters,
  replayDeadLetter
} from '../api'
import { useAuthStore } from '../store/auth'

const auth = useAuthStore()
const canWrite = computed(() => auth.isAdmin)
const loading = ref(false)
const rows = ref([])
const total = ref(0)
const selection = ref([])
const batchReplaying = ref(false)
const batchDeleting = ref(false)
const pageUseSnapshot = ref(false)
const detailUseSnapshot = ref(false)
const replayableSelection = computed(() => selection.value.filter((item) => canReplay(item)))
const dateRange = ref([])
const query = reactive({ page: 1, page_size: 20, keyword: '', status: '', forward_mode: '' })

const detailVisible = ref(false)
const detail = ref(null)
const detailReplaying = ref(false)

const statusLabels = {
  pending: '待处理',
  replaying: '重推中',
  resolved: '已解决',
  discarded: '已丢弃'
}
const statusTypes = {
  pending: 'danger',
  replaying: 'warning',
  resolved: 'success',
  discarded: 'info'
}

function statusLabel(status) {
  return statusLabels[status] || status
}
function statusType(status) {
  return statusTypes[status] || 'info'
}
function canReplay(row) {
  return row.status === 'pending' || row.status === 'discarded'
}
function consumerAvailable(row) {
  return Boolean(row.consumer_exists && !row.consumer_deleted && row.consumer_enabled)
}
function consumerStateLabel(row) {
  if (!row.consumer_exists || row.consumer_deleted) return '已删除/不存在'
  if (!row.consumer_enabled) return '已禁用'
  return '正常'
}
function consumerStateType(row) {
  return consumerAvailable(row) ? 'success' : 'danger'
}
function forwardModeLabel(mode) {
  if (mode === 'wecom_xml') return '企微兼容模式'
  if (mode === 'wecom_proxy') return '原样透传'
  if (mode === 'json') return 'JSON 模式'
  return '-'
}
function formatTime(value) {
  return value ? new Date(value).toLocaleString() : '-'
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
    const data = await listDeadLetters(params)
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
  const data = await getDeadLetter(row.id)
  detail.value = data
  detailUseSnapshot.value = false
  detailVisible.value = true
}

async function handleReplay(row, fromDrawer = false) {
  const useSnapshot = fromDrawer ? detailUseSnapshot.value : pageUseSnapshot.value
  const source = useSnapshot ? '死信创建时的历史快照' : '消费端当前最新配置'
  try {
    await ElMessageBox.confirm(`确认使用${source}重推事件「${row.event_id}」？`, '重推确认', {
      type: 'info',
      confirmButtonText: '重推',
      cancelButtonText: '取消'
    })
  } catch {
    return
  }
  if (fromDrawer) detailReplaying.value = true
  try {
    const result = await replayDeadLetter(row.id, useSnapshot)
    if (result?.task_synced === false) {
      ElMessage.warning('重推成功，但对应推送记录未找到或状态已变化')
    } else {
      ElMessage.success(
        useSnapshot ? '重推成功（使用历史快照），死信已标记为已解决' : '重推成功，死信已标记为已解决'
      )
    }
    detailVisible.value = false
    await load()
  } finally {
    detailReplaying.value = false
  }
}

async function handleBatchReplay() {
  const ids = replayableSelection.value.map((item) => item.id)
  if (ids.length === 0) {
    ElMessage.warning('请选择待处理或已丢弃的死信')
    return
  }
  if (ids.length > 50) {
    ElMessage.warning('单次最多重推 50 条')
    return
  }
  const useSnapshot = pageUseSnapshot.value
  const source = useSnapshot ? '死信创建时的历史快照' : '消费端当前最新配置'
  try {
    await ElMessageBox.confirm(`确认使用${source}批量重推选中的 ${ids.length} 条死信？`, '批量重推确认', {
      type: 'warning',
      confirmButtonText: '开始重推',
      cancelButtonText: '取消'
    })
  } catch {
    return
  }
  batchReplaying.value = true
  try {
    const result = await batchReplayDeadLetters(ids, useSnapshot)
    ElMessage.success(`重推完成：成功 ${result.success} 条，失败 ${result.failed} 条`)
    if (result.failed > 0) {
      const failed = (result.results || []).filter((item) => !item.success)
      ElMessage.warning(failed.slice(0, 3).map((item) => `#${item.id} ${item.message}`).join('；'))
    }
    await load()
  } finally {
    batchReplaying.value = false
  }
}

async function handleDiscard(row, fromDrawer = false) {
  try {
    await ElMessageBox.confirm(`确认将事件「${row.event_id}」标记为不再重推？`, '丢弃确认', {
      type: 'warning',
      confirmButtonText: '丢弃',
      cancelButtonText: '取消'
    })
  } catch {
    return
  }
  await discardDeadLetter(row.id)
  ElMessage.success('已标记为不再重推')
  if (fromDrawer) detailVisible.value = false
  await load()
}

async function handleDelete(row, fromDrawer = false) {
  try {
    await ElMessageBox.confirm(
      `确认永久删除死信「${row.event_id}」？删除后不可恢复。`,
      '删除确认',
      { type: 'warning', confirmButtonText: '永久删除', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  await deleteDeadLetter(row.id)
  ElMessage.success('已删除')
  if (fromDrawer || detail.value?.id === row.id) {
    detailVisible.value = false
  }
  await load()
}

async function handleBatchDelete() {
  if (selection.value.length === 0) {
    ElMessage.warning('请选择要删除的死信')
    return
  }
  if (selection.value.length > 100) {
    ElMessage.warning('单次最多删除 100 条')
    return
  }
  try {
    await ElMessageBox.confirm(
      `确认永久删除选中的 ${selection.value.length} 条死信？删除后不可恢复。`,
      '批量删除确认',
      { type: 'warning', confirmButtonText: '永久删除', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  batchDeleting.value = true
  try {
    const result = await batchDeleteDeadLetters(selection.value.map((item) => item.id))
    ElMessage.success(`删除完成：删除 ${result.deleted} 条，跳过 ${result.skipped} 条`)
    await load()
  } finally {
    batchDeleting.value = false
  }
}

onMounted(load)
</script>
