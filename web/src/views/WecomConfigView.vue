<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h2 class="page-title">企微参数</h2>
        <p class="page-subtitle">配置企业微信「通讯录同步」回调所使用的 Secret、Token 与 EncodingAESKey</p>
      </div>
      <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
    </div>

    <el-row :gutter="16">
      <el-col :xs="24" :md="14">
        <div class="panel">
          <el-alert
            v-if="!existing"
            type="warning"
            :closable="false"
            show-icon
            title="尚未配置企微参数"
            description="保存后，将企微后台通讯录同步回调 URL 设置为 https://你的域名/wecom/callback，并在保存时完成 URL 验证。"
            style="margin-bottom: 16px"
          />
          <el-alert
            v-else
            type="success"
            :closable="false"
            show-icon
            title="企微参数已配置"
            description="敏感字段仅显示末 4 位。修改时留空表示保持原值；修改密钥后需确保企微后台同步更新。"
            style="margin-bottom: 16px"
          />

          <el-form ref="formRef" :model="form" :rules="rules" label-width="140px">
            <el-form-item label="CorpID" prop="corp_id">
              <el-input
                v-model="form.corp_id"
                placeholder="以真实回调 ToUserName 为准，可能以 wx 或 ww 开头"
              />
            </el-form-item>
            <el-form-item label="Secret" prop="secret">
              <el-input
                v-model="form.secret"
                type="password"
                show-password
                :placeholder="maskedHint('secret_masked', '通讯录同步应用的 Secret')"
              />
            </el-form-item>
            <el-form-item label="Token" prop="token">
              <el-input
                v-model="form.token"
                type="password"
                show-password
                :placeholder="maskedHint('token_masked', '企微回调 Token')"
              />
            </el-form-item>
            <el-form-item label="EncodingAESKey" prop="encoding_aes_key">
              <el-input
                v-model="form.encoding_aes_key"
                type="password"
                show-password
                :placeholder="maskedHint('encoding_aes_key_masked', '43 位 EncodingAESKey')"
              />
              <div class="hint" style="width: 100%; margin-top: 6px">
                企微后台通讯录同步回调的 EncodingAESKey，长度为 43 位。
              </div>
            </el-form-item>

            <el-form-item v-if="canWrite">
              <el-button type="primary" :loading="saving" @click="submit">
                {{ existing ? '保存修改' : '保存配置' }}
              </el-button>
              <el-button v-if="existing" type="danger" plain @click="handleDelete">删除配置</el-button>
            </el-form-item>
            <el-alert
              v-else
              type="info"
              :closable="false"
              title="当前为只读账号，无法修改企微参数"
              show-icon
              style="margin-bottom: 16px"
            />
          </el-form>
        </div>
      </el-col>

      <el-col :xs="24" :md="10">
        <div class="panel">
          <h3 style="margin-top: 0">配置步骤</h3>
          <el-steps direction="vertical" :active="4" finish-status="success">
            <el-step title="获取企微参数" description="企微管理后台 → 应用管理 → 通讯录同步，获取 Secret、Token、EncodingAESKey。" />
            <el-step title="填写并保存" description="在左侧表单填写四项参数；已有配置时敏感字段留空表示不修改。" />
            <el-step title="配置回调 URL" description="在企微后台把回调 URL 设置为 https://域名/wecom/callback，保存时本服务会自动完成验证。" />
            <el-step title="验证消息" description="触发一次通讯录变更，确认本服务日志与消费端接收正常。" />
          </el-steps>
          <el-alert
            type="info"
            :closable="false"
            show-icon
            title="Token 与 EncodingAESKey 必须与企微后台完全一致"
            description="本服务使用它们验签解密入站事件；企微兼容模式消费端也需使用同一套密钥。"
            style="margin-top: 18px"
          />
        </div>
      </el-col>
    </el-row>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import {
  createWecomConfig,
  deleteWecomConfig,
  listWecomConfigs,
  updateWecomConfig
} from '../api'
import { useAuthStore } from '../store/auth'

const auth = useAuthStore()
const canWrite = computed(() => auth.isAdmin)
const loading = ref(false)
const saving = ref(false)
const existing = ref(null)
const formRef = ref()
const form = reactive({
  corp_id: '',
  secret: '',
  token: '',
  encoding_aes_key: ''
})

const maskedValues = computed(() => ({
  secret_masked: existing.value?.secret_masked || '',
  token_masked: existing.value?.token_masked || '',
  encoding_aes_key_masked: existing.value?.encoding_aes_key_masked || ''
}))

const rules = {
  corp_id: [{ required: true, message: '请输入 CorpID', trigger: 'blur' }]
}

function maskedHint(key, fallback) {
  const masked = maskedValues.value[key]
  return masked ? `已配置（${masked}），留空不修改` : fallback
}

async function load() {
  loading.value = true
  try {
    const data = await listWecomConfigs()
    existing.value = (data.list || [])[0] || null
    form.corp_id = existing.value?.corp_id || ''
    form.secret = ''
    form.token = ''
    form.encoding_aes_key = ''
    formRef.value?.clearValidate()
  } finally {
    loading.value = false
  }
}

async function submit() {
  await formRef.value.validate(async (valid) => {
    if (!valid) return
    if (!existing.value) {
      if (!form.secret.trim()) return ElMessage.warning('请填写 Secret')
      if (!form.token.trim()) return ElMessage.warning('请填写 Token')
      if (!form.encoding_aes_key.trim()) return ElMessage.warning('请填写 EncodingAESKey')
    }
    if (form.encoding_aes_key.trim() && form.encoding_aes_key.trim().length !== 43) {
      return ElMessage.warning('EncodingAESKey 必须是 43 位字符')
    }

    const payload = {
      corp_id: form.corp_id.trim(),
      secret: form.secret.trim(),
      token: form.token.trim(),
      encoding_aes_key: form.encoding_aes_key.trim()
    }
    saving.value = true
    try {
      if (existing.value) {
        await updateWecomConfig(existing.value.id, payload)
        ElMessage.success('企微参数已更新')
      } else {
        await createWecomConfig(payload)
        ElMessage.success('企微参数已保存')
      }
      await load()
    } finally {
      saving.value = false
    }
  })
}

async function handleDelete() {
  try {
    await ElMessageBox.confirm(
      '删除企微参数后，入站回调将无法验签解密。确认删除？',
      '删除确认',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  await deleteWecomConfig(existing.value.id)
  ElMessage.success('已删除')
  await load()
}

onMounted(load)
</script>
