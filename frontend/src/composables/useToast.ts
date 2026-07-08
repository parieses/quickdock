import { reactive } from 'vue'

interface ToastMessage {
  id: number
  text: string
  type: 'error' | 'success'
}

interface ConfirmItem {
  id: number
  message: string
  resolve: (value: boolean) => void
}

const state = reactive<{ items: ToastMessage[] }>({ items: [] })
const confirmState = reactive<{ items: ConfirmItem[] }>({ items: [] })
let nextId = 1

export function useToast() {
  function show(text: string, type: 'error' | 'success' = 'error') {
    const id = nextId++
    state.items.push({ id, text, type })
    setTimeout(() => {
      const idx = state.items.findIndex(m => m.id === id)
      if (idx !== -1) state.items.splice(idx, 1)
    }, 3000)
  }

  function error(text: string) { show(text, 'error') }
  function success(text: string) { show(text, 'success') }

  function remove(id: number) {
    const idx = state.items.findIndex(m => m.id === id)
    if (idx !== -1) state.items.splice(idx, 1)
  }

  // Promise-based confirm，替代阻塞主线程的 window.confirm()
  function confirm(message: string): Promise<boolean> {
    return new Promise((resolve) => {
      const id = nextId++
      confirmState.items.push({ id, message, resolve })
    })
  }

  function resolveConfirm(id: number, value: boolean) {
    const idx = confirmState.items.findIndex(c => c.id === id)
    if (idx !== -1) {
      confirmState.items[idx].resolve(value)
      confirmState.items.splice(idx, 1)
    }
  }

  return { items: state.items, show, error, success, remove, confirm, confirmItems: confirmState.items, resolveConfirm }
}
