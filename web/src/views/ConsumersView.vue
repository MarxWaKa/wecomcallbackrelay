<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h2 class="page-title">消费端管理</h2>
        <p class="page-subtitle">事件将异步转发给所有启用的消费端；支持 JSON 与企微兼容两种模式</p>
      </div>
      <el-button v-if="canWrite" type="primary" :icon="Plus" @click="openCreate">新增消费端</el-button>
    </div>

    <div class="panel">
      <div class="toolbar">
        <el-input
          v-model="query.keyword"
          placeholder="搜索名称或回调地址"
          clearable
          style="width: 240px"
          @keyup.enter="search"
          @clear="search"
        />
        <el-select v-model="query.forward_mode" placeholder="转发模式" clearable style="width: 150px" @change="search">
          <el-option label="JSON 模式" value="json" />
          <el-option label="企微兼容模式" value="wecom_xml" />
          <el-option label="原样透传" value="wecom_proxy" />
        </el-select>
        <el-select v-model="query.enabled" placeholder="启用状态" clearable style="width: 130px" @change="search">
          <el-option label="已启用" value="true" />
          <el-option label="已禁用" value="false" />
        </el-select>
        <el-button :icon="Search" @click="search">查询</el-button>
        <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="name" label="名称" min-width="140" show-overflow-tooltip />
        <el-table-column prop="callback_url" label="回调地址" min-width="240" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="mono">{{ row.callback_url }}</span>
          </template>
        </el-table-column>
        <el-table-column label="转发模式" width="120" align="center">
          <template #default="{ row }">
            <el-tag v-if="row.forward_mode === 'wecom_xml'" type="warning" effect="light">企微兼容</el-tag>
            <el-tag v-else-if="row.forward_mode === 'wecom_proxy'" type="success" effect="light">原样透传</el-tag>
            <el-tag v-else type="primary" effect="light">JSON</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="认证" width="100" align="center">
          <template #default="{ row }">
            <span v-if="row.forward_mode === 'wecom_xml'" class="hint">企微签名</span>
            <span v-else-if="row.forward_mode === 'wecom_proxy'" class="hint">—</span>
            <span v-else class="hint">{{ authLabel(row.auth_type) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="90" align="center">
          <template #default="{ row }">
            <el-tag :type="row.enabled ? 'success' : 'info'" effect="plain">
              {{ row.enabled ? '启用' : '禁用' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="超时/重试" width="110" align="center">
          <template #default="{ row }">
            <span class="hint">{{ row.timeout_ms }}ms / {{ row.max_retry }} 次</span>
          </template>
        </el-table-column>
        <el-table-column prop="remark" label="备注" min-width="120" show-overflow-tooltip />
        <el-table-column label="操作" width="220" fixed="right" align="center">
          <template #default="{ row }">
            <template v-if="canWrite">
              <el-button link type="primary" :loading="testingId === row.id" @click="handleTest(row)">测试</el-button>
              <el-button link type="primary" @click="openEdit(row)">编辑</el-button>
              <el-button link type="danger" @click="handleDelete(row)">删除</el-button>
            </template>
            <span v-else class="hint">只读</span>
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

    <el-dialog
      v-model="dialogVisible"
      :title="editingId ? '编辑消费端' : '新增消费端'"
      width="720px"
      top="6vh"
      destroy-on-close
    >
      <el-form ref="formRef" :model="form" :rules="rules" label-width="150px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" maxlength="100" placeholder="例如：Moka HR" />
        </el-form-item>
        <el-form-item label="回调地址" prop="callback_url">
          <el-input v-model="form.callback_url" placeholder="https://consumer.example.com/wecom/callback" />
        </el-form-item>
        <el-form-item label="转发模式" prop="forward_mode">
          <el-radio-group v-model="form.forward_mode" @change="onModeChange">
            <el-radio-button value="json">JSON 模式</el-radio-button>
            <el-radio-button value="wecom_xml">企微兼容模式</el-radio-button>
            <el-radio-button value="wecom_proxy">原样透传</el-radio-button>
          </el-radio-group>
          <div class="hint" style="width: 100%; margin-top: 6px">
            {{
              form.forward_mode === 'json'
                ? '转发解密后的 JSON，消费端使用自定义认证；适合可改造的新系统。'
                : form.forward_mode === 'wecom_xml'
                  ? '重新加密为企微 XML 并附加新签名参数；消费端保留原验签解密代码即可，实现无感切换。'
                  : '不解密、不重新加密，直接把企微原始加密请求原样转发；消费端必须与「企微参数」使用同一套 Token/AESKey/CorpID。'
            }}
          </div>
        </el-form-item>

        <template v-if="form.forward_mode === 'json'">
          <el-form-item label="认证方式" prop="auth_type">
            <el-select v-model="form.auth_type" style="width: 220px" @change="onAuthTypeChange">
              <el-option label="无认证" value="none" />
              <el-option label="Bearer Token" value="bearer" />
              <el-option label="Basic Auth" value="basic" />
              <el-option label="HMAC-SHA256（推荐）" value="hmac" />
            </el-select>
          </el-form-item>
          <el-form-item v-if="form.auth_type === 'bearer'" label="Bearer Token">
            <el-input
              v-model="form.auth_token"
              type="password"
              show-password
              :placeholder="authHint('token')"
            />
          </el-form-item>
          <template v-if="form.auth_type === 'basic'">
            <el-form-item label="用户名">
              <el-input v-model="form.auth_username" :placeholder="authHint('username') || 'Basic 用户名'" />
            </el-form-item>
            <el-form-item label="密码">
              <el-input v-model="form.auth_password" type="password" show-password :placeholder="authHint('password')" />
            </el-form-item>
          </template>
          <el-form-item v-if="form.auth_type === 'hmac'" label="HMAC Secret">
            <el-input
              v-model="form.auth_secret"
              type="password"
              show-password
              :placeholder="authHint('secret')"
            />
            <div class="hint" style="width: 100%; margin-top: 6px">
              请求头 X-Timestamp 与 X-Signature = HMAC-SHA256(secret, timestamp + "." + body)
            </div>
          </el-form-item>
        </template>

        <template v-else-if="form.forward_mode === 'wecom_xml'">
          <el-divider content-position="left">企微兼容模式密钥</el-divider>
          <el-alert
            type="info"
            :closable="false"
            title="必须与存量消费端当前使用的 Token / EncodingAESKey 完全一致；转发时使用该密钥重新加密。"
            show-icon
            style="margin-bottom: 14px"
          />
          <el-form-item label="企微 Token">
            <el-input
              v-model="form.wecom_token"
              type="password"
              show-password
              :placeholder="maskedHint(tokenMasked, '请输入与企微后台一致的 Token')"
            />
          </el-form-item>
          <el-form-item label="EncodingAESKey">
            <el-input
              v-model="form.wecom_encoding_aes_key"
              type="password"
              show-password
              :placeholder="maskedHint(aesKeyMasked, '43 位 EncodingAESKey')"
            />
          </el-form-item>
          <el-form-item label="CorpID">
            <el-input
              v-model="form.wecom_corp_id"
              placeholder="消费端解密库使用的 receiveID（以真实回调 ToUserName 为准）"
            />
          </el-form-item>
          <el-form-item label="AgentID（可选）">
            <el-input
              v-model="form.wecom_agent_id"
              placeholder="消费端按 AgentID 路由时填写；真实事件会自动透传入站 AgentID"
            />
            <div class="hint" style="width: 100%; margin-top: 6px">
              企微直连报文外层通常包含 AgentID。真实事件转发时会自动透传入站 AgentID；此字段仅用于“测试”按钮的模拟事件。
            </div>
          </el-form-item>
          <el-form-item label="密钥指纹">
            <span class="mono">{{ wecomFingerprint || '尚未配置' }}</span>
            <div class="hint" style="width: 100%; margin-top: 6px">
              用于与消费端核对密钥是否一致（不泄露密钥）。消费端可执行：
              <code>printf 'wecom-relay|%s\0%s' "$TOKEN" "$AESKEY" | sha256sum | cut -c1-12</code>
            </div>
          </el-form-item>
        </template>
        <template v-else>
          <el-divider content-position="left">原样透传</el-divider>
          <el-alert
            type="warning"
            :closable="false"
            show-icon
            title="消费端必须与「企微参数」使用同一套 Token/EncodingAESKey/CorpID"
            description="中间件不会重新加密，而是把企微原始加密 Body 和 msg_signature/timestamp/nonce 原样转发。若消费端密钥与入站企微参数不一致，将无法解密。"
            style="margin-bottom: 14px"
          />
          <el-form-item label="AgentID（测试用）">
            <el-input
              v-model="form.wecom_agent_id"
              placeholder="可选；仅用于“测试”按钮生成的样本报文"
            />
          </el-form-item>
        </template>

        <el-divider content-position="left">投递参数</el-divider>
        <el-row :gutter="16">
          <el-col :span="12">
            <el-form-item label="超时时间(ms)">
              <el-input-number v-model="form.timeout_ms" :min="1000" :max="60000" :step="500" style="width: 100%" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="最大重试次数">
              <el-input-number v-model="form.max_retry" :min="1" :max="20" style="width: 100%" />
            </el-form-item>
          </el-col>
        </el-row>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="form.remark" maxlength="255" placeholder="可选" />
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="submit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, ElNotification } from 'element-plus'
import { Plus, Refresh, Search } from '@element-plus/icons-vue'
import {
  createConsumer,
  deleteConsumer,
  listConsumers,
  testConsumer,
  updateConsumer
} from '../api'
import { useAuthStore } from '../store/auth'

const auth = useAuthStore()
const canWrite = computed(() => auth.isAdmin)
const loading = ref(false)
const rows = ref([])
const total = ref(0)
const testingId = ref(0)
const query = reactive({ page: 1, page_size: 20, keyword: '', forward_mode: '', enabled: '' })

const dialogVisible = ref(false)
const saving = ref(false)
const editingId = ref(0)
const formRef = ref()
const tokenMasked = ref('')
const aesKeyMasked = ref('')
const wecomFingerprint = ref('')
const authHints = reactive({ token: '', username: '', password: '', secret: '' })
const originalAuthType = ref('none')

const emptyForm = () => ({
  name: '',
  callback_url: '',
  forward_mode: 'json',
  auth_type: 'none',
  auth_token: '',
  auth_username: '',
  auth_password: '',
  auth_secret: '',
  wecom_token: '',
  wecom_encoding_aes_key: '',
  wecom_corp_id: '',
  wecom_agent_id: '',
  enabled: true,
  timeout_ms: 5000,
  max_retry: 5,
  remark: ''
})
const form = reactive(emptyForm())

const rules = {
  name: [{ required: true, message: '请输入消费端名称', trigger: 'blur' }],
  callback_url: [
    { required: true, message: '请输入回调地址', trigger: 'blur' },
    {
      pattern: /^https?:\/\/[^\s]+$/i,
      message: '请输入合法的 http/https 地址',
      trigger: 'blur'
    }
  ]
}

function authLabel(type) {
  return { none: '无认证', bearer: 'Bearer', basic: 'Basic', hmac: 'HMAC' }[type] || type || '-'
}

function maskedHint(masked, fallback) {
  return masked ? `已配置（${masked}），留空不修改` : fallback
}

function authHint(key) {
  return authHints[key] || '留空表示不修改（新增时必填）'
}

async function load() {
  loading.value = true
  try {
    const params = {
      page: query.page,
      page_size: query.page_size,
      keyword: query.keyword || undefined,
      forward_mode: query.forward_mode || undefined
    }
    if (query.enabled !== '') params.enabled = query.enabled
    const data = await listConsumers(params)
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

function resetForm() {
  Object.assign(form, emptyForm())
  tokenMasked.value = ''
  aesKeyMasked.value = ''
  wecomFingerprint.value = ''
  Object.assign(authHints, { token: '', username: '', password: '', secret: '' })
  originalAuthType.value = 'none'
  formRef.value?.clearValidate()
}

function openCreate() {
  editingId.value = 0
  resetForm()
  dialogVisible.value = true
}

function openEdit(row) {
  editingId.value = row.id
  resetForm()
  Object.assign(form, {
    name: row.name,
    callback_url: row.callback_url,
    forward_mode: row.forward_mode,
    auth_type: row.forward_mode === 'json' ? row.auth_type : 'none',
    auth_token: '',
    auth_username: row.auth_config?.username || '',
    auth_password: '',
    auth_secret: '',
    wecom_token: '',
    wecom_encoding_aes_key: '',
    wecom_corp_id: row.wecom_corp_id || '',
    wecom_agent_id: row.wecom_agent_id || '',
    enabled: row.enabled,
    timeout_ms: row.timeout_ms,
    max_retry: row.max_retry,
    remark: row.remark || ''
  })
  originalAuthType.value = form.auth_type
  tokenMasked.value = row.wecom_token_masked || ''
  aesKeyMasked.value = row.wecom_encoding_aes_key_masked || ''
  wecomFingerprint.value = row.wecom_key_fingerprint || ''
  authHints.token = row.auth_config?.token || ''
  authHints.username = row.auth_config?.username || ''
  authHints.password = row.auth_config?.password || ''
  authHints.secret = row.auth_config?.secret || ''
  dialogVisible.value = true
}

function onModeChange() {
  if (form.forward_mode !== 'json') {
    form.auth_type = 'none'
  }
}

function onAuthTypeChange() {
  form.auth_token = ''
  form.auth_password = ''
  form.auth_secret = ''
}

function validateConditionalFields() {
  if (form.forward_mode === 'wecom_proxy') {
    return ''
  }
  if (form.forward_mode === 'wecom_xml') {
    if (!editingId.value) {
      if (!form.wecom_token.trim()) return '请填写企微 Token'
      if (!form.wecom_encoding_aes_key.trim()) return '请填写 EncodingAESKey'
    } else {
      if (!form.wecom_token.trim() && !tokenMasked.value) return '请填写企微 Token'
      if (!form.wecom_encoding_aes_key.trim() && !aesKeyMasked.value) return '请填写 EncodingAESKey'
    }
    if (!form.wecom_corp_id.trim()) return '请填写 CorpID'
    return ''
  }

  const authChanged = editingId.value > 0 && form.auth_type !== originalAuthType.value
  if (form.auth_type === 'bearer') {
    if (!form.auth_token.trim() && (!authHints.token || authChanged)) return '请填写 Bearer Token'
  }
  if (form.auth_type === 'basic') {
    if (!form.auth_username.trim()) return '请填写 Basic 用户名'
    if (!form.auth_password.trim() && (!authHints.password || authChanged)) return '请填写 Basic 密码'
  }
  if (form.auth_type === 'hmac') {
    if (!form.auth_secret.trim() && (!authHints.secret || authChanged)) return '请填写 HMAC Secret'
  }
  return ''
}

async function submit() {
  await formRef.value.validate(async (valid) => {
    if (!valid) return
    const conditionalError = validateConditionalFields()
    if (conditionalError) {
      ElMessage.warning(conditionalError)
      return
    }

    const payload = {
      name: form.name.trim(),
      callback_url: form.callback_url.trim(),
      forward_mode: form.forward_mode,
      auth_type: form.forward_mode === 'json' ? form.auth_type : 'none',
      auth_config: {},
      wecom_token: form.wecom_token.trim(),
      wecom_encoding_aes_key: form.wecom_encoding_aes_key.trim(),
      wecom_corp_id: form.wecom_corp_id.trim(),
      wecom_agent_id: form.wecom_agent_id.trim(),
      enabled: form.enabled,
      timeout_ms: form.timeout_ms,
      max_retry: form.max_retry,
      remark: form.remark.trim()
    }
    if (form.forward_mode === 'json') {
      if (form.auth_type === 'bearer') payload.auth_config = { token: form.auth_token.trim() }
      if (form.auth_type === 'basic') {
        payload.auth_config = {
          username: form.auth_username.trim(),
          password: form.auth_password.trim()
        }
      }
      if (form.auth_type === 'hmac') payload.auth_config = { secret: form.auth_secret.trim() }
    }

    saving.value = true
    try {
      if (editingId.value) {
        await updateConsumer(editingId.value, payload)
        ElMessage.success('消费端已更新')
      } else {
        await createConsumer(payload)
        ElMessage.success('消费端已创建')
      }
      dialogVisible.value = false
      load()
    } finally {
      saving.value = false
    }
  })
}

async function handleTest(row) {
  testingId.value = row.id
  try {
    const result = await testConsumer(row.id)
    if (result.success) {
      ElMessage.success(`测试成功（HTTP ${result.status_code}，${result.latency_ms}ms）`)
    } else {
      const detail = [
        result.status_code ? `HTTP ${result.status_code}` : '',
        result.error || '未知错误',
        result.corp_id ? `CorpID=${result.corp_id}` : '',
        result.key_fingerprint ? `密钥指纹=${result.key_fingerprint}` : ''
      ]
        .filter(Boolean)
        .join('；')
      ElNotification.error({ title: '测试失败', message: detail, duration: 15000 })
    }
  } finally {
    testingId.value = 0
  }
}

async function handleDelete(row) {
  try {
    await ElMessageBox.confirm(
      `确认删除消费端「${row.name}」？删除后将不再向该地址转发新事件。`,
      '删除确认',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  await deleteConsumer(row.id)
  ElMessage.success('已删除')
  load()
}

onMounted(load)
</script>
