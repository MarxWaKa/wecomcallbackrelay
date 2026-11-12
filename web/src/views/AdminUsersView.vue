<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h2 class="page-title">管理员</h2>
        <p class="page-subtitle">管理后台账号与角色；viewer 角色仅可查看，不能修改配置或重推死信</p>
      </div>
      <el-button type="primary" :icon="Plus" @click="openCreate">新增管理员</el-button>
    </div>

    <div class="panel">
      <div class="toolbar">
        <el-input
          v-model="query.keyword"
          placeholder="搜索用户名或昵称"
          clearable
          style="width: 240px"
          @keyup.enter="search"
          @clear="search"
        />
        <el-button :icon="Search" @click="search">查询</el-button>
        <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="id" label="ID" width="70" align="center" />
        <el-table-column prop="username" label="用户名" min-width="130" />
        <el-table-column prop="nickname" label="昵称" min-width="130" />
        <el-table-column label="角色" width="100" align="center">
          <template #default="{ row }">
            <el-tag :type="row.role === 'admin' ? 'danger' : 'info'" effect="light">
              {{ row.role === 'admin' ? '管理员' : '只读' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="90" align="center">
          <template #default="{ row }">
            <el-tag :type="row.enabled ? 'success' : 'info'" effect="plain">
              {{ row.enabled ? '启用' : '禁用' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="最后登录" width="180" align="center">
          <template #default="{ row }">{{ row.last_login_at ? new Date(row.last_login_at).toLocaleString() : '-' }}</template>
        </el-table-column>
        <el-table-column label="操作" width="260" fixed="right" align="center">
          <template #default="{ row }">
            <el-button link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button link type="primary" @click="openReset(row)">重置密码</el-button>
            <el-button link type="danger" :disabled="row.id === currentUserId" @click="handleDelete(row)">删除</el-button>
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

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑管理员' : '新增管理员'" width="480px" destroy-on-close>
      <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
        <el-form-item v-if="!editingId" label="用户名" prop="username">
          <el-input v-model="form.username" placeholder="至少 3 个字符" />
        </el-form-item>
        <el-form-item v-if="!editingId" label="初始密码" prop="password">
          <el-input v-model="form.password" type="password" show-password placeholder="至少 8 个字符" />
        </el-form-item>
        <el-form-item label="昵称">
          <el-input v-model="form.nickname" maxlength="64" />
        </el-form-item>
        <el-form-item label="角色">
          <el-radio-group v-model="form.role">
            <el-radio value="admin">管理员</el-radio>
            <el-radio value="viewer">只读</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" :disabled="editingId === currentUserId" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="resetVisible" title="重置密码" width="420px" destroy-on-close>
      <el-form ref="resetFormRef" :model="resetForm" :rules="resetRules" label-width="90px">
        <el-form-item label="新密码" prop="new_password">
          <el-input v-model="resetForm.new_password" type="password" show-password placeholder="至少 8 个字符" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="resetVisible = false">取消</el-button>
        <el-button type="primary" :loading="resetting" @click="submitReset">确认重置</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Search } from '@element-plus/icons-vue'
import {
  createAdminUser,
  deleteAdminUser,
  listAdminUsers,
  resetAdminPassword,
  updateAdminUser
} from '../api'
import { useAuthStore } from '../store/auth'

const auth = useAuthStore()
const currentUserId = computed(() => auth.user?.id)

const loading = ref(false)
const rows = ref([])
const total = ref(0)
const query = reactive({ page: 1, page_size: 20, keyword: '' })

const dialogVisible = ref(false)
const saving = ref(false)
const editingId = ref(0)
const formRef = ref()
const form = reactive({ username: '', password: '', nickname: '', role: 'admin', enabled: true })

const rules = {
  username: [
    { required: true, message: '请输入用户名', trigger: 'blur' },
    { min: 3, message: '用户名至少 3 个字符', trigger: 'blur' }
  ],
  password: [
    { required: true, message: '请输入密码', trigger: 'blur' },
    { min: 8, message: '密码至少 8 个字符', trigger: 'blur' }
  ]
}

const resetVisible = ref(false)
const resetting = ref(false)
const resetFormRef = ref()
const resetTarget = ref(null)
const resetForm = reactive({ new_password: '' })
const resetRules = {
  new_password: [
    { required: true, message: '请输入新密码', trigger: 'blur' },
    { min: 8, message: '密码至少 8 个字符', trigger: 'blur' }
  ]
}

async function load() {
  loading.value = true
  try {
    const data = await listAdminUsers({
      page: query.page,
      page_size: query.page_size,
      keyword: query.keyword || undefined
    })
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

function openCreate() {
  editingId.value = 0
  Object.assign(form, { username: '', password: '', nickname: '', role: 'admin', enabled: true })
  formRef.value?.clearValidate()
  dialogVisible.value = true
}

function openEdit(row) {
  editingId.value = row.id
  Object.assign(form, {
    username: row.username,
    password: '',
    nickname: row.nickname || '',
    role: row.role,
    enabled: row.enabled
  })
  formRef.value?.clearValidate()
  dialogVisible.value = true
}

async function submit() {
  await formRef.value.validate(async (valid) => {
    if (!valid) return
    saving.value = true
    try {
      if (editingId.value) {
        await updateAdminUser(editingId.value, {
          nickname: form.nickname.trim(),
          role: form.role,
          enabled: form.enabled
        })
        ElMessage.success('管理员已更新')
      } else {
        await createAdminUser({
          username: form.username.trim(),
          password: form.password,
          nickname: form.nickname.trim(),
          role: form.role,
          enabled: form.enabled
        })
        ElMessage.success('管理员已创建')
      }
      dialogVisible.value = false
      await load()
      if (editingId.value === currentUserId.value) {
        await auth.refreshUser()
      }
    } finally {
      saving.value = false
    }
  })
}

function openReset(row) {
  resetTarget.value = row
  resetForm.new_password = ''
  resetFormRef.value?.clearValidate()
  resetVisible.value = true
}

async function submitReset() {
  await resetFormRef.value.validate(async (valid) => {
    if (!valid) return
    resetting.value = true
    try {
      await resetAdminPassword(resetTarget.value.id, { new_password: resetForm.new_password })
      ElMessage.success('密码已重置')
      resetVisible.value = false
    } finally {
      resetting.value = false
    }
  })
}

async function handleDelete(row) {
  try {
    await ElMessageBox.confirm(`确认删除管理员「${row.username}」？`, '删除确认', {
      type: 'warning',
      confirmButtonText: '删除',
      cancelButtonText: '取消'
    })
  } catch {
    return
  }
  await deleteAdminUser(row.id)
  ElMessage.success('已删除')
  await load()
}

onMounted(load)
</script>
