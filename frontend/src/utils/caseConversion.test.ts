import { describe, it, expect } from 'vitest'
import { camelToSnake, snakeToCamel, toSnakeCase, toCamelCase } from './caseConversion'

// 前后端字段名映射的唯一约定：Go 侧 json tag 是蛇形，前端用驼峰。
// 这里锁死边界行为（尤其是缩写 ID/URL/HTTP），一旦回归会导致接口字段静默丢失。
describe('camelToSnake', () => {
  const cases: [string, string][] = [
    ['createdAt', 'created_at'],
    ['openStrategy', 'open_strategy'],
    ['workspaceId', 'workspace_id'],
    ['WorkspaceID', 'workspace_id'],
    ['URL', 'url'],
    ['HTTPServer', 'http_server'],
    ['ID', 'id'],
    ['name', 'name'],
    ['firstName', 'first_name'],
  ]

  for (const [input, expected] of cases) {
    it(`${input} -> ${expected}`, () => {
      expect(camelToSnake(input)).toBe(expected)
    })
  }

  it('首字符大写不产生前导下划线', () => {
    expect(camelToSnake('Name')).toBe('name')
  })

  it('缩写后跟小写时不拆开（HTTPServer 而非 h_t_t_p_server）', () => {
    expect(camelToSnake('APIKey')).toBe('api_key')
  })
})

describe('snakeToCamel', () => {
  it('转换下划线后的首字母', () => {
    expect(snakeToCamel('created_at')).toBe('createdAt')
    expect(snakeToCamel('workspace_id')).toBe('workspaceId')
  })

  it('无下划线时原样返回', () => {
    expect(snakeToCamel('name')).toBe('name')
  })
})

describe('对象 key 批量转换', () => {
  it('toSnakeCase 转换所有 key 并保留值', () => {
    expect(toSnakeCase({ createdAt: 1, openStrategy: 'x' })).toEqual({ created_at: 1, open_strategy: 'x' })
  })

  it('toCamelCase 转换所有 key 并保留值', () => {
    expect(toCamelCase({ created_at: 1, workspace_id: 'w' })).toEqual({ createdAt: 1, workspaceId: 'w' })
  })

  it('空对象返回空对象', () => {
    expect(toSnakeCase({})).toEqual({})
    expect(toCamelCase({})).toEqual({})
  })
})
