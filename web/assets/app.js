      const { createApp, onMounted, ref, computed } = Vue
      const apiBase = '/api'

      createApp({
        setup() {
          const tasks = ref([])
          const items = ref([]) // 商品目录
          const selectedTaskId = ref(null)
          const token = ref(localStorage.getItem('token') || '')
          const userEmail = ref(localStorage.getItem('user_email') || '')
          const userRole = ref(localStorage.getItem('user_role') || '')
          const loading = ref({ create: false, toggle: false, delete: false, auth: false })
          const authForm = ref({ email: '', password: '', invite_code: '' })
          const authTab = ref('login')
          const showAuth = ref(false)
          const verifyCode = ref('')
          const resendCountdown = ref(0)
          const confirmModal = ref({ show: false, title: '', message: '' })
          const toast = ref({ show: false, message: '', type: 'success' })
          const notifyToast = ref({ show: false, message: '' })
          const newItemDuration = ref(10 * 60 * 1000)
          const guestHeartbeatMs = ref(5 * 60 * 1000)
          const maxTasksPerUser = ref(3)
          let resendTimer = null
          let confirmAction = null
          const seenItemIds = new Set()
          const form = ref({
            keyword: '',
            min_price: 0,
            max_price: 0,
            sort: 'created_time|desc',
            platform: 1,
          })
          const fallbackImage = 'https://via.placeholder.com/300x180.png?text=GoodsHunter'

          const apiUrl = (path) => `${apiBase}${path}`

          const authHeaders = () => {
            return token.value ? { Authorization: `Bearer ${token.value}` } : {}
          }

          const isGuest = computed(() => userRole.value === 'guest')
          const userBadge = computed(() => (userEmail.value ? userEmail.value.charAt(0).toUpperCase() : '?'))

          const fetchTasks = async () => {
            try {
              const res = await fetch(apiUrl('/tasks'), { headers: authHeaders() })
              if (res.ok) {
                const data = await res.json()
                tasks.value = (data || []).map((task) => ({
                  ...task,
                  notify_enabled: typeof task.notify_enabled === 'boolean' ? task.notify_enabled : true,
                }))
                if (tasks.value.length === 0) {
                  selectedTaskId.value = null
                } else if (!selectedTaskId.value || !tasks.value.find((t) => t.id === selectedTaskId.value)) {
                  selectedTaskId.value = tasks.value[0].id
                }
              }
            } catch (e) {
              console.error(e)
            }
            fetchTimeline()
          }

          const fetchConfig = async () => {
            if (!token.value) return
            try {
              const res = await fetch(apiUrl('/config'), { headers: authHeaders() })
              if (res.ok) {
                const data = await res.json()
                if (data.new_item_duration_ms) {
                  newItemDuration.value = data.new_item_duration_ms
                }
                if (data.guest_heartbeat_ms) {
                  guestHeartbeatMs.value = data.guest_heartbeat_ms
                }
                if (typeof data.max_tasks_per_user === 'number') {
                  maxTasksPerUser.value = data.max_tasks_per_user
                }
              }
            } catch (e) {
              console.error('fetch config failed', e)
            }
          }

          const showToast = (message, type = 'success') => {
            toast.value = { show: true, message, type }
            setTimeout(() => {
              toast.value.show = false
            }, 2000)
          }

          const openConfirm = (title, message, onConfirm) => {
            confirmModal.value = { show: true, title, message }
            confirmAction = onConfirm
          }

          const confirmOk = () => {
            confirmModal.value.show = false
            if (confirmAction) confirmAction()
            confirmAction = null
          }

          const confirmCancel = () => {
            confirmModal.value.show = false
            confirmAction = null
          }

          const register = async () => {
            if (!authForm.value.invite_code) {
              showToast('请输入邀请码', 'error')
              return
            }
            loading.value.auth = true
            try {
              const res = await fetch(apiUrl('/register'), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(authForm.value),
              })
              if (!res.ok) throw new Error('注册失败')
              startCountdown()
              showToast('验证码已发送，请查收邮箱')
            } catch (e) {
              showToast(e.message, 'error')
            } finally {
              loading.value.auth = false
            }
          }

          const login = async () => {
            loading.value.auth = true
            try {
              const res = await fetch(apiUrl('/login'), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(authForm.value),
              })
              if (!res.ok) throw new Error('登录失败')
              const data = await res.json()
              token.value = data.token
              localStorage.setItem('token', token.value)
              userEmail.value = authForm.value.email
              userRole.value = 'admin'
              localStorage.setItem('user_email', userEmail.value)
              localStorage.setItem('user_role', userRole.value)
              await fetchConfig()
              await fetchTasks()
              showAuth.value = false
            } catch (e) {
              showToast(e.message, 'error')
            } finally {
              loading.value.auth = false
            }
          }

          const guestLogin = async () => {
            loading.value.auth = true
            try {
              const res = await fetch(apiUrl('/login/guest'), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
              })
              if (!res.ok) throw new Error('游客登录失败')
              const data = await res.json()
              token.value = data.token
              localStorage.setItem('token', token.value)
              userEmail.value = 'demo@goodshunter.com'
              userRole.value = 'guest'
              localStorage.setItem('user_email', userEmail.value)
              localStorage.setItem('user_role', userRole.value)
              await fetchConfig()
              await fetchTasks()
              showAuth.value = false
            } catch (e) {
              showToast(e.message, 'error')
            } finally {
              loading.value.auth = false
            }
          }

          const logout = () => {
            fetch(apiUrl('/logout'), { method: 'POST', headers: authHeaders() }).finally(() => {
              token.value = ''
              localStorage.removeItem('token')
              userEmail.value = ''
              userRole.value = ''
              localStorage.removeItem('user_email')
              localStorage.removeItem('user_role')
              tasks.value = []
              items.value = []
              selectedTaskId.value = null
            })
          }

          const deleteAccount = () => {
            openConfirm('注销账号', '确认注销账号？该账号下所有任务和关联数据将被删除。', () => {
              fetch(apiUrl('/me/delete'), { method: 'POST', headers: authHeaders() })
                .then((res) => {
                  if (!res.ok) throw new Error('注销失败')
                  token.value = ''
                  localStorage.removeItem('token')
                  userEmail.value = ''
                  userRole.value = ''
                  localStorage.removeItem('user_email')
                  localStorage.removeItem('user_role')
                  tasks.value = []
                  items.value = []
                  selectedTaskId.value = null
                  showAuth.value = false
                  showToast('账号已注销')
                })
                .catch((e) => showToast(e.message, 'error'))
            })
          }

          const verifyEmail = async () => {
            loading.value.auth = true
            try {
              const res = await fetch(apiUrl('/verify'), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ email: authForm.value.email, code: verifyCode.value }),
              })
              if (!res.ok) throw new Error('验证码无效或已过期')
              authTab.value = 'login'
              showToast('验证成功，请登录')
            } catch (e) {
              showToast(e.message, 'error')
            } finally {
              loading.value.auth = false
            }
          }

          const resendCode = async () => {
            if (resendCountdown.value > 0) return
            loading.value.auth = true
            try {
              const res = await fetch(apiUrl('/resend'), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ email: authForm.value.email }),
              })
              if (!res.ok) throw new Error('重发失败')
              startCountdown()
            } catch (e) {
              showToast(e.message, 'error')
            } finally {
              loading.value.auth = false
            }
          }

          const startCountdown = () => {
            resendCountdown.value = 60
            if (resendTimer) clearInterval(resendTimer)
            resendTimer = setInterval(() => {
              resendCountdown.value -= 1
              if (resendCountdown.value <= 0) {
                clearInterval(resendTimer)
              }
            }, 1000)
          }

          const createTask = async () => {
            if (isGuest.value) {
              showToast('演示模式无权操作', 'error')
              return
            }
            if (tasks.value.length >= 3) {
              showToast('每个账号最多只能创建 3 个任务', 'error')
              return
            }
            loading.value.create = true
            try {
              const payload = {
                keyword: form.value.keyword,
                min_price: Number(form.value.min_price),
                max_price: Number(form.value.max_price),
                sort: form.value.sort,
                platform: Number(form.value.platform),
              }
              const res = await fetch(apiUrl('/tasks'), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json', ...authHeaders() },
                body: JSON.stringify(payload),
              })
              if (!res.ok) throw new Error('创建任务失败')
              await fetchTasks()
              form.value.keyword = ''
            } catch (e) {
              showToast(e.message, 'error')
            } finally {
              loading.value.create = false
            }
          }

          const deleteTask = async (task) => {
            if (isGuest.value) {
              showToast('演示模式无权操作', 'error')
              return
            }
            openConfirm('删除任务', `确认删除该任务吗？`, async () => {
              loading.value.delete = true
              try {
                const res = await fetch(apiUrl(`/tasks/${task.id}`), { method: 'DELETE', headers: authHeaders() })
                if (!res.ok) throw new Error('删除任务失败')
                await fetchTasks()
                if (selectedTaskId.value === task.id) {
                  selectedTaskId.value = tasks.value[0]?.id || null
                  fetchTimeline()
                }
              } catch (e) {
                showToast(e.message, 'error')
              } finally {
                loading.value.delete = false
              }
            })
          }

          const toggleTask = async (task) => {
            loading.value.toggle = true
            try {
              const nextStatus = task.status === 'running' ? 'stopped' : 'running'
              const res = await fetch(apiUrl(`/tasks/${task.id}/status`), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json', ...authHeaders() },
                body: JSON.stringify({ status: nextStatus }),
              })
              if (!res.ok) throw new Error('状态更新失败')
              task.status = nextStatus
            } catch (e) {
              showToast(e.message, 'error')
            } finally {
              loading.value.toggle = false
            }
          }

          const toggleNotify = async (task) => {
            const nextEnabled = !task.notify_enabled
            const previous = task.notify_enabled
            task.notify_enabled = nextEnabled
            try {
              const res = await fetch(apiUrl(`/tasks/${task.id}/notify`), {
                method: 'PATCH',
                headers: { 'Content-Type': 'application/json', ...authHeaders() },
                body: JSON.stringify({ enabled: nextEnabled }),
              })
              if (!res.ok) throw new Error('更新通知失败')
            } catch (e) {
              task.notify_enabled = previous
              showToast(e.message, 'error')
            }
          }

          const fetchTimeline = async () => {
            if (!token.value) {
              items.value = []
              return
            }
            if (!selectedTaskId.value) {
              items.value = []
              return
            }
            try {
              const params = new URLSearchParams({
                limit: 100,
                task_id: selectedTaskId.value,
                _t: Date.now(), // 防止缓存
              })
              const res = await fetch(apiUrl(`/timeline?${params.toString()}`), { headers: authHeaders() })
              if (res.ok) {
                const data = await res.json()
                const incoming = data.items || []
                items.value = incoming

                // 检测新商品并弹窗提醒 (对所有用户生效)
                const fresh = incoming.find((item) => {
                  if (!item || !item.id) return false
                  if (seenItemIds.has(item.id)) return false
                  return item.is_new === true
                })
                incoming.forEach((item) => item && item.id && seenItemIds.add(item.id))
                if (fresh) {
                  notifyToast.value = {
                    show: true,
                    message: `🎉 发现新商品: ${fresh.title} - ¥${fresh.price}`,
                  }
                  setTimeout(() => {
                    notifyToast.value.show = false
                  }, 3500)
                }
              }
            } catch (e) {
              console.error(e)
            }
          }

          const selectTask = (task) => {
            selectedTaskId.value = task.id
            fetchTimeline()
          }

          onMounted(() => {
            if (token.value) {
              fetchConfig()
              fetchTasks()
              fetchTimeline()
            }
            setInterval(fetchTimeline, 5000)
            setInterval(() => {
              if (token.value && userRole.value === 'guest') {
                fetchConfig()
              }
            }, guestHeartbeatMs.value)
          })

          const priceRange = (t) => {
            const min = t.min_price || 0
            const max = t.max_price || 0
            if (min && max) return `${min} - ${max}`
            if (min) return `>= ${min}`
            if (max) return `<= ${max}`
            return '未设定'
          }

          const isNewItem = (item) => {
            if (!item) return false
            if (typeof item.is_new === 'boolean') return item.is_new
            if (!item.created_at) return false
            const created = new Date(item.created_at)
            if (isNaN(created.getTime())) return false
            const diffMs = Date.now() - created.getTime()
            // 使用动态配置的时间
            return diffMs >= 0 && diffMs <= newItemDuration.value
          }

          return {
            tasks,
            items,
            form,
            loading,
            selectedTaskId,
            maxTasksPerUser,
            token,
            userEmail,
            userRole,
            userBadge,
            isGuest,
            authForm,
            authTab,
            showAuth,
            verifyCode,
            resendCountdown,
            confirmModal,
            toast,
            notifyToast,
            createTask,
            toggleTask,
            deleteTask,
            selectTask,
            register,
            login,
            guestLogin,
            logout,
            deleteAccount,
            verifyEmail,
            resendCode,
            confirmOk,
            confirmCancel,
            toggleNotify,
            priceRange,
            isNewItem,
            fallbackImage,
          }
        },
      }).mount('#app')
