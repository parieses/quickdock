// 统一驼峰/蛇形转换工具
// 前后端共享转换规则，确保字段名映射一致

// knownAcronyms 已识别的全大写缩写（值 = 蛇形后缀，不含前导下划线）
const knownAcronyms: Record<string, string> = {
  ID: 'id',
  URL: 'url',
  API: 'api',
  IP: 'ip',
  HTTP: 'http',
  JSON: 'json',
  UUID: 'uuid',
  OS: 'os',
  URI: 'uri',
  SQL: 'sql',
  CPU: 'cpu',
  IO: 'io',
}

/**
 * camelToSnake 驼峰 → 蛇形
 * 示例：createdAt → created_at, openStrategy → open_strategy, WorkspaceID → workspace_id
 */
export function camelToSnake(str: string): string {
  // 若整体就是已知缩写（如 "ID" 本身），直接返回蛇形小写
  if (knownAcronyms[str]) {
    return knownAcronyms[str]
  }

  const result: string[] = []
  for (let i = 0; i < str.length; i++) {
    const char = str[i]
    const upper = char.toUpperCase()
    const lower = char.toLowerCase()

    // 大写字母
    if (char === upper && char !== lower) {
      // 检查是否命中已知缩写
      let consumed = false
      for (const [acro, snake] of Object.entries(knownAcronyms)) {
        if (str.slice(i).startsWith(acro)) {
          const next = i + acro.length
          // 边界检查：缩写后是结尾 / 非大写 / 或「大写开头但接下来是小写」
          if (
            next >= str.length ||
            str[next] < 'A' || str[next] > 'Z' ||
            (next + 1 >= str.length || (str[next + 1] >= 'a' && str[next + 1] <= 'z'))
          ) {
            if (result.length > 0) result.push('_')
            result.push(snake)
            i += acro.length - 1
            consumed = true
            break
          }
        }
      }
      if (consumed) continue

      // 普通大写字母：前面加下划线（首字符除外）
      if (result.length > 0) result.push('_')
      result.push(lower)
    } else {
      result.push(char)
    }
  }
  return result.join('')
}

/**
 * snakeToCamel 蛇形 → 驼峰
 * 示例：created_at → createdAt, open_strategy → openStrategy, workspace_id → workspaceId
 */
export function snakeToCamel(str: string): string {
  return str.replace(/_([a-z])/g, (_, c) => c.toUpperCase())
}

/**
 * toSnakeCase 对象所有 key 转蛇形
 */
export function toSnakeCase(obj: Record<string, any>): Record<string, any> {
  const out: Record<string, any> = {}
  for (const [k, v] of Object.entries(obj)) {
    out[camelToSnake(k)] = v
  }
  return out
}

/**
 * toCamelCase 对象所有 key 转驼峰
 */
export function toCamelCase(obj: Record<string, any>): Record<string, any> {
  const out: Record<string, any> = {}
  for (const [k, v] of Object.entries(obj)) {
    out[snakeToCamel(k)] = v
  }
  return out
}

// 测试用例
if (typeof window !== 'undefined') {
  // 仅在前端环境运行测试
  const tests = [
    { input: 'createdAt', expected: 'created_at' },
    { input: 'openStrategy', expected: 'open_strategy' },
    { input: 'workspaceId', expected: 'workspace_id' },
    { input: 'WorkspaceID', expected: 'workspace_id' },
    { input: 'URL', expected: 'url' },
    { input: 'HTTPServer', expected: 'http_server' },
    { input: 'ID', expected: 'id' },
    { input: 'name', expected: 'name' },
    { input: 'firstName', expected: 'first_name' },
  ]

  let passed = 0
  let failed = 0
  for (const test of tests) {
    const result = camelToSnake(test.input)
    if (result === test.expected) {
      passed++
    } else {
      console.error(`FAIL: camelToSnake('${test.input}') = '${result}', expected '${test.expected}'`)
      failed++
    }
  }
  console.log(`camelToSnake tests: ${passed} passed, ${failed} failed`)
}
