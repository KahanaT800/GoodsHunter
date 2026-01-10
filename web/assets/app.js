      const { createApp, onMounted, ref, computed } = Vue
      const apiBase = '/api'
      const localeMap = {
        zh: 'zh-CN',
        en: 'en',
        ja: 'ja-JP',
      }
      const messages = {
        zh: {
          common: {
            appName: 'GoodsHunter',
            notLoggedIn: '未登录',
            visitorMode: '访客模式（只读）',
          },
          auth: {
            login: '登录',
            register: '注册',
            email: '邮箱',
            password: '密码',
            passwordHint: '密码至少 6 位',
            inviteCode: '邀请码',
            registerSend: '注册并发送验证码',
            verifyCode: '验证码',
            verify: '验证邮箱',
            resend: '重发验证码',
            resendCountdown: '重发({seconds}s)',
            guest: '无需注册，立即体验',
            loggedIn: '已登录',
            logout: '退出登录',
            deleteAccount: '注销账号',
          },
          task: {
            manage: '任务管理',
            keyword: '关键词',
            keywordPlaceholder: '例如 初音ミク フィギュア',
            platform: '平台',
            minPrice: '最低价 (JPY)',
            maxPrice: '最高价 (JPY)',
            sort: '排序方式',
            sortNewest: '按最新上架',
            sortPriceAsc: '按价格降序',
            sortPriceDesc: '按价格升序',
            create: '新建任务',
            creating: '提交中...',
            list: '任务列表',
            empty: '暂无任务',
            notify: '通知',
            edit: '编辑',
            stop: '停止',
            start: '启动',
            delete: '删除',
            adjustPrice: '调整价格区间',
            minPriceShort: '最低价',
            maxPriceShort: '最高价',
            save: '保存',
            cancel: '取消',
          },
          items: {
            title: '在售商品',
            selectTask: '请先选择一个任务',
            noItems: '当前条件暂无在售商品',
            failed: '抓取商品失败，请稍后再试',
            empty: '暂无商品数据',
            startHint: '任务启动后将立即开始爬取',
            view: '前往商品',
            new: 'NEW',
            idLabel: 'ID',
            notifyTitle: 'GoodsHunter 通知',
          },
          confirm: {
            ok: '确定',
            cancel: '取消',
            deleteTaskTitle: '删除任务',
            deleteTaskMessage: '确认删除该任务吗？',
            deleteAccountTitle: '注销账号',
            deleteAccountMessage: '确认注销账号？该账号下所有任务和关联数据将被删除。',
          },
          priceRange: {
            none: '未设定',
            min: '>= {min}',
            max: '<= {max}',
            between: '{min} - {max}',
          },
          errors: {
            inviteRequired: '请输入邀请码',
            registerFailed: '注册失败',
            loginFailed: '登录失败',
            guestLoginFailed: '游客登录失败',
            codeSent: '验证码已发送，请查收邮箱',
            verifyFailed: '验证码无效或已过期',
            verifySuccess: '验证成功，请登录',
            resendFailed: '重发失败',
            demoNoPermission: '演示模式无权操作',
            maxTasks: '每个账号最多只能创建 {count} 个任务',
            createFailed: '创建任务失败',
            updateFailed: '更新任务失败',
            taskUpdated: '任务已更新',
            deleteFailed: '删除任务失败',
            statusFailed: '状态更新失败',
            updateNotifyFailed: '更新通知失败',
            priceNegative: '价格不能为负数',
            priceInvalid: '最低价不能大于最高价',
            accountDeleted: '账号已注销',
            accountDeleteFailed: '注销失败',
            timelineFailed: '加载时间线失败',
            newItem: '🎉 发现新商品: {title} - ¥{price}',
          },
        },
        en: {
          common: {
            appName: 'GoodsHunter',
            notLoggedIn: 'Not logged in',
            visitorMode: 'Visitor Mode (ReadOnly)',
          },
          auth: {
            login: 'Log In',
            register: 'Register',
            email: 'Email',
            password: 'Password',
            passwordHint: 'Password must be at least 6 characters',
            inviteCode: 'Invite Code',
            registerSend: 'Register & Send Code',
            verifyCode: 'Verification Code',
            verify: 'Verify Email',
            resend: 'Resend Code',
            resendCountdown: 'Resend ({seconds}s)',
            guest: 'Try as Guest',
            loggedIn: 'Logged in',
            logout: 'Log out',
            deleteAccount: 'Delete Account',
          },
          task: {
            manage: 'Task Manager',
            keyword: 'Keyword',
            keywordPlaceholder: 'e.g. Hatsune Miku figure',
            platform: 'Platform',
            minPrice: 'Min Price (JPY)',
            maxPrice: 'Max Price (JPY)',
            sort: 'Sort',
            sortNewest: 'Newest',
            sortPriceAsc: 'Price: Desc',
            sortPriceDesc: 'Price: Asc',
            create: 'Create Task',
            creating: 'Submitting...',
            list: 'Task List',
            empty: 'No tasks',
            notify: 'Notify',
            edit: 'Edit',
            stop: 'Stop',
            start: 'Start',
            delete: 'Delete',
            adjustPrice: 'Adjust Price Range',
            minPriceShort: 'Min',
            maxPriceShort: 'Max',
            save: 'Save',
            cancel: 'Cancel',
          },
          items: {
            title: 'Items on Sale',
            selectTask: 'Select a task first',
            noItems: 'No items match the current criteria',
            failed: 'Fetch failed, please try again',
            empty: 'No item data',
            startHint: 'Crawling starts as soon as the task runs',
            view: 'View Item',
            new: 'NEW',
            idLabel: 'ID',
            notifyTitle: 'GoodsHunter Notification',
          },
          confirm: {
            ok: 'Confirm',
            cancel: 'Cancel',
            deleteTaskTitle: 'Delete Task',
            deleteTaskMessage: 'Delete this task?',
            deleteAccountTitle: 'Delete Account',
            deleteAccountMessage: 'Delete this account? All tasks and data will be removed.',
          },
          priceRange: {
            none: 'Not set',
            min: '>= {min}',
            max: '<= {max}',
            between: '{min} - {max}',
          },
          errors: {
            inviteRequired: 'Please enter the invite code',
            registerFailed: 'Registration failed',
            loginFailed: 'Login failed',
            guestLoginFailed: 'Guest login failed',
            codeSent: 'Code sent. Please check your email.',
            verifyFailed: 'Code invalid or expired',
            verifySuccess: 'Verified. Please log in.',
            resendFailed: 'Resend failed',
            demoNoPermission: 'Demo mode is read-only',
            maxTasks: 'Max {count} tasks per account',
            createFailed: 'Create task failed',
            updateFailed: 'Update task failed',
            taskUpdated: 'Task updated',
            deleteFailed: 'Delete task failed',
            statusFailed: 'Status update failed',
            updateNotifyFailed: 'Notification update failed',
            priceNegative: 'Price cannot be negative',
            priceInvalid: 'Min price cannot exceed max price',
            accountDeleted: 'Account deleted',
            accountDeleteFailed: 'Account deletion failed',
            timelineFailed: 'Timeline load failed',
            newItem: '🎉 New item: {title} - ¥{price}',
          },
        },
        ja: {
          common: {
            appName: 'GoodsHunter',
            notLoggedIn: '未ログイン',
            visitorMode: '閲覧モード（読取専用）',
          },
          auth: {
            login: 'ログイン',
            register: '新規登録',
            email: 'メール',
            password: 'パスワード',
            passwordHint: 'パスワードは6文字以上',
            inviteCode: '招待コード',
            registerSend: '登録してコード送信',
            verifyCode: '認証コード',
            verify: 'メール確認',
            resend: '再送信',
            resendCountdown: '再送信({seconds}s)',
            guest: '登録不要で体験',
            loggedIn: 'ログイン済み',
            logout: 'ログアウト',
            deleteAccount: 'アカウント削除',
          },
          task: {
            manage: 'タスク管理',
            keyword: 'キーワード',
            keywordPlaceholder: '例：初音ミク フィギュア',
            platform: 'プラットフォーム',
            minPrice: '最低価格 (JPY)',
            maxPrice: '最高価格 (JPY)',
            sort: '並び順',
            sortNewest: '新着順',
            sortPriceAsc: '価格: 高い順',
            sortPriceDesc: '価格: 安い順',
            create: '新規タスク',
            creating: '送信中...',
            list: 'タスクリスト',
            empty: 'タスクなし',
            notify: '通知',
            edit: '編集',
            stop: '停止',
            start: '開始',
            delete: '削除',
            adjustPrice: '価格帯を調整',
            minPriceShort: '最低',
            maxPriceShort: '最高',
            save: '保存',
            cancel: 'キャンセル',
          },
          items: {
            title: '販売中の商品',
            selectTask: '先にタスクを選択してください',
            noItems: '条件に合う商品がありません',
            failed: '取得に失敗しました。後ほど再試行してください',
            empty: '商品データがありません',
            startHint: 'タスク開始後すぐにクロールします',
            view: '商品ページへ',
            new: 'NEW',
            idLabel: 'ID',
            notifyTitle: 'GoodsHunter 通知',
          },
          confirm: {
            ok: 'OK',
            cancel: 'キャンセル',
            deleteTaskTitle: 'タスク削除',
            deleteTaskMessage: 'このタスクを削除しますか？',
            deleteAccountTitle: 'アカウント削除',
            deleteAccountMessage: 'アカウントを削除しますか？このアカウントのタスクと関連データは削除されます。',
          },
          priceRange: {
            none: '未設定',
            min: '>= {min}',
            max: '<= {max}',
            between: '{min} - {max}',
          },
          errors: {
            inviteRequired: '招待コードを入力してください',
            registerFailed: '登録に失敗しました',
            loginFailed: 'ログインに失敗しました',
            guestLoginFailed: 'ゲストログインに失敗しました',
            codeSent: '認証コードを送信しました。メールを確認してください。',
            verifyFailed: 'コードが無効または期限切れです',
            verifySuccess: '認証成功。ログインしてください。',
            resendFailed: '再送信に失敗しました',
            demoNoPermission: 'デモモードでは操作できません',
            maxTasks: '1アカウントあたり最大{count}件',
            createFailed: 'タスク作成に失敗しました',
            updateFailed: 'タスク更新に失敗しました',
            taskUpdated: 'タスクを更新しました',
            deleteFailed: 'タスク削除に失敗しました',
            statusFailed: 'ステータス更新に失敗しました',
            updateNotifyFailed: '通知更新に失敗しました',
            priceNegative: '価格は負数にできません',
            priceInvalid: '最低価格は最高価格を超えられません',
            accountDeleted: 'アカウントを削除しました',
            accountDeleteFailed: 'アカウント削除に失敗しました',
            timelineFailed: 'タイムラインの読み込みに失敗しました',
            newItem: '🎉 新商品: {title} - ¥{price}',
          },
        },
      }

      const detectLang = () => {
        const saved = localStorage.getItem('lang')
        if (saved) return saved
        const nav = (navigator.language || '').toLowerCase()
        if (nav.startsWith('ja')) return 'ja'
        if (nav.startsWith('en')) return 'en'
        return 'zh'
      }

      createApp({
        setup() {
          const tasks = ref([])
          const items = ref([]) // 商品目录
          const timelineStatus = ref('')
          const timelineMessage = ref('')
          const selectedTaskId = ref(null)
          const lang = ref(detectLang())
          const token = ref(localStorage.getItem('token') || '')
          const userEmail = ref(localStorage.getItem('user_email') || '')
          const userRole = ref(localStorage.getItem('user_role') || '')
          const loading = ref({ create: false, toggle: false, delete: false, edit: false, auth: false })
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
          const editingTaskId = ref(null)
          const editForm = ref({ min_price: 0, max_price: 0 })
          const fallbackImage = 'https://via.placeholder.com/300x180.png?text=GoodsHunter'

          const apiUrl = (path) => `${apiBase}${path}`

          const t = (key, params = {}) => {
            const lookup = (dict) => key.split('.').reduce((acc, part) => (acc ? acc[part] : undefined), dict)
            const template = lookup(messages[lang.value]) || lookup(messages.zh) || key
            return String(template).replace(/\{(\w+)\}/g, (_, k) => (params[k] !== undefined ? params[k] : `{${k}}`))
          }

          const setLang = () => {
            localStorage.setItem('lang', lang.value)
            document.documentElement.lang = localeMap[lang.value] || 'zh-CN'
          }

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
              showToast(t('errors.inviteRequired'), 'error')
              return
            }
            loading.value.auth = true
            try {
              const res = await fetch(apiUrl('/register'), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(authForm.value),
              })
              if (!res.ok) throw new Error(t('errors.registerFailed'))
              startCountdown()
              showToast(t('errors.codeSent'))
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
              if (!res.ok) throw new Error(t('errors.loginFailed'))
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
              if (!res.ok) throw new Error(t('errors.guestLoginFailed'))
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
            openConfirm(t('confirm.deleteAccountTitle'), t('confirm.deleteAccountMessage'), () => {
              fetch(apiUrl('/me/delete'), { method: 'POST', headers: authHeaders() })
                .then((res) => {
                  if (!res.ok) throw new Error(t('errors.accountDeleteFailed'))
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
                  showToast(t('errors.accountDeleted'))
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
              if (!res.ok) throw new Error(t('errors.verifyFailed'))
              authTab.value = 'login'
              showToast(t('errors.verifySuccess'))
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
              if (!res.ok) throw new Error(t('errors.resendFailed'))
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
              showToast(t('errors.demoNoPermission'), 'error')
              return
            }
            if (tasks.value.length >= maxTasksPerUser.value) {
              showToast(t('errors.maxTasks', { count: maxTasksPerUser.value }), 'error')
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
              if (!res.ok) throw new Error(t('errors.createFailed'))
              await fetchTasks()
              form.value.keyword = ''
            } catch (e) {
              showToast(e.message, 'error')
            } finally {
              loading.value.create = false
            }
          }

          const startEditTask = (task) => {
            console.log('startEditTask called', task.id)
            if (isGuest.value) {
              showToast(t('errors.demoNoPermission'), 'error')
              return
            }
            editingTaskId.value = task.id
            editForm.value = {
              min_price: Number(task.min_price || 0),
              max_price: Number(task.max_price || 0),
            }
          }

          const cancelEditTask = () => {
            editingTaskId.value = null
          }

          const saveEditTask = async (task) => {
            console.log('saveEditTask', task.id, editForm.value)
            if (isGuest.value) {
              showToast(t('errors.demoNoPermission'), 'error')
              return
            }
            const minPrice = Number(editForm.value.min_price || 0)
            const maxPrice = Number(editForm.value.max_price || 0)
            if (minPrice < 0 || maxPrice < 0) {
              showToast(t('errors.priceNegative'), 'error')
              return
            }
            if (minPrice && maxPrice && minPrice > maxPrice) {
              showToast(t('errors.priceInvalid'), 'error')
              return
            }
            loading.value.edit = true
            try {
              const payload = {
                min_price: minPrice,
                max_price: maxPrice,
              }
              const res = await fetch(apiUrl(`/tasks/${task.id}`), {
                method: 'PATCH',
                headers: { 'Content-Type': 'application/json', ...authHeaders() },
                body: JSON.stringify(payload),
              })
              if (!res.ok) throw new Error(t('errors.updateFailed'))
              task.min_price = minPrice
              task.max_price = maxPrice
              editingTaskId.value = null
              showToast(t('errors.taskUpdated'))
            } catch (e) {
              showToast(e.message, 'error')
            } finally {
              loading.value.edit = false
            }
          }

          const deleteTask = async (task) => {
            if (isGuest.value) {
              showToast(t('errors.demoNoPermission'), 'error')
              return
            }
            openConfirm(t('confirm.deleteTaskTitle'), t('confirm.deleteTaskMessage'), async () => {
              loading.value.delete = true
              try {
                const res = await fetch(apiUrl(`/tasks/${task.id}`), { method: 'DELETE', headers: authHeaders() })
                if (!res.ok) throw new Error(t('errors.deleteFailed'))
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
              if (!res.ok) throw new Error(t('errors.statusFailed'))
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
              if (!res.ok) throw new Error(t('errors.updateNotifyFailed'))
            } catch (e) {
              task.notify_enabled = previous
              showToast(e.message, 'error')
            }
          }

          const fetchTimeline = async () => {
            if (!token.value) {
              items.value = []
              timelineStatus.value = ''
              timelineMessage.value = ''
              return
            }
            if (!selectedTaskId.value) {
              items.value = []
              timelineStatus.value = ''
              timelineMessage.value = ''
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
                timelineStatus.value = data.status || ''
                timelineMessage.value = data.message || ''

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
                    message: t('errors.newItem', { title: fresh.title, price: fresh.price }),
                  }
                  setTimeout(() => {
                    notifyToast.value.show = false
                  }, 3500)
                }
              }
            } catch (e) {
              console.error(e)
              timelineStatus.value = 'failed'
              timelineMessage.value = t('errors.timelineFailed')
            }
          }

          const selectTask = (task) => {
            selectedTaskId.value = task.id
            fetchTimeline()
          }

          onMounted(() => {
            setLang()
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

          const priceRange = (task) => {
            const min = task.min_price || 0
            const max = task.max_price || 0
            if (min && max) return t('priceRange.between', { min, max })
            if (min) return t('priceRange.min', { min })
            if (max) return t('priceRange.max', { max })
            return t('priceRange.none')
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
            timelineStatus,
            timelineMessage,
            form,
            editingTaskId,
            editForm,
            loading,
            selectedTaskId,
            maxTasksPerUser,
            lang,
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
            t,
            setLang,
            createTask,
            startEditTask,
            cancelEditTask,
            saveEditTask,
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
