<template>
  <div class="login-page">
    <div class="login-card">
      <div class="login-brand">
        <div class="logo">企</div>
        <div>
          <h1>企微通讯录回调转发</h1>
          <p>wecom-callback-relay 管理后台</p>
        </div>
      </div>

      <el-form ref="formRef" :model="form" :rules="rules" label-position="top" @keyup.enter="submit">
        <el-form-item label="用户名" prop="username">
          <el-input v-model="form.username" size="large" placeholder="请输入管理员账号" autocomplete="username" />
        </el-form-item>
        <el-form-item label="密码" prop="password">
          <el-input
            v-model="form.password"
            size="large"
            type="password"
            show-password
            placeholder="请输入密码"
            autocomplete="current-password"
          />
        </el-form-item>
        <el-button type="primary" size="large" :loading="loading" style="width: 100%" @click="submit">
          登 录
        </el-button>
      </el-form>

      <p class="hint" style="margin-top: 18px">
        账号由系统初始化时创建，首次部署请使用 .env 中的 ADMIN_USERNAME / ADMIN_PASSWORD。
      </p>
    </div>
  </div>
</template>

<script setup>
import { reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { login } from '../api'
import { useAuthStore } from '../store/auth'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const formRef = ref()
const loading = ref(false)
const form = reactive({ username: '', password: '' })
const rules = {
  username: [{ required: true, message: '请输入用户名', trigger: 'blur' }],
  password: [{ required: true, message: '请输入密码', trigger: 'blur' }]
}

async function submit() {
  await formRef.value.validate(async (valid) => {
    if (!valid) return
    loading.value = true
    try {
      const data = await login({ username: form.username.trim(), password: form.password })
      auth.setLogin(data)
      ElMessage.success('登录成功')
      const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/dashboard'
      router.replace(redirect)
    } finally {
      loading.value = false
    }
  })
}
</script>
