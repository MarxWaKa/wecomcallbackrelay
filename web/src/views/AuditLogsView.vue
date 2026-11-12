<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h2 class="page-title">操作审计</h2>
        <p class="page-subtitle">记录配置变更与死信重推等管理端关键动作</p>
      </div>
      <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
    </div>

    <div class="panel">
      <div class="toolbar">
        <el-input
          v-model="query.keyword"
          placeholder="动作或详情关键字"
          clearable
          style="width: 240px"
          @keyup.enter="search"
          @clear="search"
        />
        <el-input
          v-model="query.admin_name"
          placeholder="操作人"
          clearable
          style="width: 180px"
          @keyup.enter="search"
          @clear="search"
        />
        <el-button :icon="Search" @click="search">查询</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="id" label="ID" width="70" align="center" />
        <el-table-column prop="admin_name" label="操作人" width="130" />
        <el-table-column prop="action" label="动作" min-width="190">
          <template #default="{ row }">
            <span class="mono">{{ row.action }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="target_type" label="对象" width="130" />
        <el-table-column prop="target_id" label="对象 ID" width="90" align="center" />
        <el-table-column prop="detail" label="详情" min-width="240" show-overflow-tooltip />
        <el-table-column label="结果" width="90" align="center">
          <template #default="{ row }">
            <el-tag :type="row.result === 'success' ? 'success' : 'danger'" effect="light">
              {{ row.result === 'success' ? '成功' : '失败' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="时间" width="180" align="center">
          <template #default="{ row }">{{ new Date(row.created_at).toLocaleString() }}</template>
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
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { Refresh, Search } from '@element-plus/icons-vue'
import { listAuditLogs } from '../api'

const loading = ref(false)
const rows = ref([])
const total = ref(0)
const query = reactive({ page: 1, page_size: 20, keyword: '', admin_name: '' })

async function load() {
  loading.value = true
  try {
    const data = await listAuditLogs({
      page: query.page,
      page_size: query.page_size,
      keyword: query.keyword || undefined,
      admin_name: query.admin_name || undefined
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

onMounted(load)
</script>
