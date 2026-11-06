import { describe, it, expect } from 'vitest'
import { getErrorMessage, isDialogCanceled } from './error'

// Wails 绑定抛出的错误形态不统一（JSON 字符串 / 带前缀文本 / 普通对象 / 字符串），
// 这里锁死每种形态的提取结果——提取错了，用户看到的就是一坨 JSON 而不是人话。
describe('getErrorMessage', () => {
  it('null / undefined 返回空串', () => {
    expect(getErrorMessage(null)).toBe('')
    expect(getErrorMessage(undefined)).toBe('')
  })

  it('Error.message 是 JSON 时提取 .message', () => {
    const e = new Error('{"message":"名称已存在","cause":{},"kind":"RuntimeError"}')
    expect(getErrorMessage(e)).toBe('名称已存在')
  })

  it('去掉 Bound method returned an error 前缀', () => {
    expect(getErrorMessage(new Error('Bound method returned an error: 名称已存在'))).toBe('名称已存在')
  })

  it('前缀 + JSON 组合时先去前缀再解析', () => {
    const e = new Error('Bound method returned an error: {"message":"端口被占用"}')
    expect(getErrorMessage(e)).toBe('端口被占用')
  })

  it('普通对象取 message 字段', () => {
    expect(getErrorMessage({ message: '名称已存在', kind: 'RuntimeError' })).toBe('名称已存在')
  })

  it('普通字符串原样返回', () => {
    expect(getErrorMessage('出错了')).toBe('出错了')
  })

  it('JSON 但没有 message 字段时返回原文', () => {
    expect(getErrorMessage('{"code":1}')).toBe('{"code":1}')
  })

  it('以 { 开头但不是合法 JSON 时返回原文，不抛异常', () => {
    expect(getErrorMessage('{oops')).toBe('{oops')
  })
})

// 取消是用户主动动作，不是失败：识别不出来就会每按一次取消弹一个红 toast。
describe('isDialogCanceled', () => {
  it('识别 Wails 的 cancelled by user（Windows 拼法）', () => {
    const e = new Error('Invalid dialog call: Dialog.OpenFile failed: error getting selection: cancelled by user')
    expect(isDialogCanceled(e)).toBe(true)
  })

  it('识别 canceled by user（macOS 拼法）', () => {
    expect(isDialogCanceled(new Error('error getting selection: canceled by user'))).toBe(true)
  })

  it('其他对话框错误不算取消', () => {
    expect(isDialogCanceled(new Error('Invalid dialog call: no window available'))).toBe(false)
  })

  it('普通业务错误不算取消', () => {
    expect(isDialogCanceled(new Error('路径不存在'))).toBe(false)
    expect(isDialogCanceled(null)).toBe(false)
  })
})
