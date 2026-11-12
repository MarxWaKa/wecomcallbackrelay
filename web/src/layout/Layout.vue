<template>
  <el-container class="layout">
    <el-aside width="220px" class="aside">
      <div class="brand">
        <div class="brand-logo">企</div>
        <div>
          <div class="brand-title">回调转发中台</div>
          <div class="brand-sub">WeCom Relay</div>
        </div>
      </div>
      <el-menu
        :default-active="route.path"
        router
        class="menu"
        background-color="#0f172a"
        text-color="#cbd5e1"
        active-text-color="#ffffff"
      >
        <el-menu-item v-for="item in menuItems" :key="item.path" :index="item.path">
          <el-icon><component :is="item.icon" /></el-icon>
          <span>{{ item.label }}</span>
        </el-menu-item>
      </el-menu>
    </el-aside>

    <el-container>
      <el-header class="header">
        <div class="header-title">{{ route.meta.title || '概览' }}</div>
        <div class="header-right">
          <el-tag size="small" :type="auth.isAdmin ? 'danger' : 'info'" effect="light">
            {{ auth.isAdmin ? '管理员' : '只读账号' }}
          </el-tag>
          <el-dropdown @command="onCommand">
            <span class="user-trigger">
              {{ auth.user?.nickname || auth.user?.username || '未登录' }}
              <el-icon><ArrowDown /></el-icon>
            </span>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="password">修改密码</el-dropdown-item>
                <el-dropdown-item command="logout" divided>退出登录</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </el-header>

      <el-main class="main">
        <router-view />
      </el-main>
    </el-container>

    <el-dialog v-model="passwordVisible" title="修改密码" width="420px" destroy-on-close>
      <el-form ref="passwordFormRef" :model="passwordForm" :rules="passwordRules" label-width="90px">
        <el-form-item label="原密码" prop="old_password">
          <el-input v-model="passwordForm.old_password" type="password" show-password />
        </el-form-item>
        <el-form-item label="新密码" prop="new_password">
          <el-input v-model="passwordForm.new_password" type="password" show-password />
        </el-form-item>
        <el-form-item label="确认密码" prop="confirm">
          <el-input v-model="passwordForm.confirm" type="password" show-password />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="passwordVisible = false">取消</el-button>
        <el-button type="primary" :loading="passwordSaving" @click="submitPassword">确认修改</el-button>
      </template>
    </el-dialog>
  </el-container>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useAuthStore } from '../store/auth'
import { changePassword, logout } from '../api'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const allMenuItems = [
  { path: '/dashboard', label: '概览', icon: 'Odometer' },
  { path: '/consumers', label: '消费端管理', icon: 'Connection' },
  { path: '/wecom-config', label: '企微参数', icon: 'Key' },
  { path: '/dead-letters', label: '死信管理', icon: 'Warning' },
  { path: '/admin-users', label: '管理员', icon: 'User', adminOnly: true },
  { path: '/audit-logs', label: '操作审计', icon: 'Document' }
]

const menuItems = computed(() => allMenuItems.filter((item) => !item.adminOnly || auth.isAdmin))

onMounted(async () => {
  try {
    await auth.refreshUser()
  } catch {
    // 401 已由拦截器统一处理
  }
})

const passwordVisible = ref(false)
const passwordSaving = ref(false)
const passwordFormRef = ref()
const passwordForm = reactive({ old_password: '', new_password: '', confirm: '' })
const passwordRules = {
  old_password: [{ required: true, message: '请输入原密码', trigger: 'blur' }],
  new_password: [
    { required: true, message: '请输入新密码', trigger: 'blur' },
    { min: 8, message: '新密码至少 8 个字符', trigger: 'blur' }
  ],
  confirm: [
    { required: true, message: '请再次输入新密码', trigger: 'blur' },
    {
      validator: (rule, value, callback) => {
        if (value !== passwordForm.new_password) {
          callback(new Error('两次输入的密码不一致'))
        } else {
          callback()
        }
      },
      trigger: 'blur'
    }
  ]
}

async function onCommand(command) {
  if (command === 'password') {
    passwordForm.old_password = ''
    passwordForm.new_password = ''
    passwordForm.confirm = ''
    passwordVisible.value = true
    return
  }
  if (command === 'logout') {
    try {
      await logout()
    } catch {
      // 登出失败不影响本地清理
    }
    auth.clear()
    router.replace('/login')
  }
}

async function submitPassword() {
  await passwordFormRef.value.validate(async (valid) => {
    if (!valid) return
    passwordSaving.value = true
    try {
      await changePassword({
        old_password: passwordForm.old_password,
        new_password: passwordForm.new_password
      })
      ElMessage.success('密码已修改，请重新登录')
      passwordVisible.value = false
      auth.clear()
      router.replace('/login')
    } finally {
      passwordSaving.value = false
    }
  })
}
</script>

<style scoped>
.layout {
  min-height: 100vh;
}

.aside {
  background: #0f172a;
  color: #e2e8f0;
  display: flex;
  flex-direction: column;
}

.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 18px 16px;
  border-bottom: 1px solid rgba(148, 163, 184, 0.16);
}

.brand-logo {
  width: 36px;
  height: 36px;
  border-radius: 10px;
  background: linear-gradient(135deg, #2563eb, #0ea5e9);
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 700;
}

.brand-title {
  font-size: 14px;
  font-weight: 600;
}

.brand-sub {
  font-size: 11px;
  color: #94a3b8;
  letter-spacing: 0.08em;
}

.menu {
  border-right: none;
  flex: 1;
}

.header {
  height: 60px;
  background: #fff;
  border-bottom: 1px solid #e5e7eb;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 20px;
}

.header-title {
  font-size: 16px;
  font-weight: 600;
}

.header-right {
  display: flex;
  align-items: center;
  gap: 14px;
}

.user-trigger {
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: #334155;
  font-size: 14px;
  outline: none;
}

.main {
  padding: 0;
  background: var(--relay-bg);
}
</style>
