import type { GroupPlatform } from '@/types'

export const OPENAI_CC_SWITCH_CODEX_MODEL = 'gpt-5.6-sol'
export const OPENAI_CC_SWITCH_CODEX_REVIEW_MODEL = 'gpt-5.6-luna'
export const OPENAI_CC_SWITCH_CODEX_REASONING_EFFORT = 'high'
export const GROK_CC_SWITCH_MODEL = 'grok-4.5'

export type CcSwitchClientType = 'claude' | 'gemini'

export interface CcSwitchImportConfig {
  app: string
  endpoint: string
  model?: string
}

export interface CcSwitchImportDeeplinkInput {
  baseUrl: string
  platform?: GroupPlatform | null
  clientType: CcSwitchClientType
  providerName: string
  apiKey: string
  usageScript: string
}

function withV1Endpoint(baseUrl: string): string {
  const normalizedBaseUrl = baseUrl.replace(/\/+$/, '')
  return normalizedBaseUrl.endsWith('/v1') ? normalizedBaseUrl : `${normalizedBaseUrl}/v1`
}

export function resolveCcSwitchImportConfig(
  platform: GroupPlatform | undefined | null,
  clientType: CcSwitchClientType,
  baseUrl: string
): CcSwitchImportConfig {
  switch (platform || 'anthropic') {
    case 'antigravity':
      return {
        app: clientType === 'gemini' ? 'gemini' : 'claude',
        endpoint: `${baseUrl}/antigravity`
      }
    case 'openai':
      return {
        app: 'codex',
        endpoint: baseUrl,
        model: OPENAI_CC_SWITCH_CODEX_MODEL
      }
    case 'gemini':
      return {
        app: 'gemini',
        endpoint: baseUrl
      }
    case 'grok':
      return {
        app: 'grokbuild',
        endpoint: withV1Endpoint(baseUrl),
        model: GROK_CC_SWITCH_MODEL
      }
    default:
      return {
        app: 'claude',
        endpoint: baseUrl
      }
  }
}

// 将包含中文的配置按 UTF-8 编码为 CCS 协议要求的 Base64。
function encodeBase64Utf8(value: string): string {
  const bytes = new TextEncoder().encode(value)
  let binary = ''

  for (const byte of bytes) {
    binary += String.fromCharCode(byte)
  }

  return btoa(binary)
}

function toTomlString(value: string): string {
  return JSON.stringify(value) ?? '""'
}

// 生成 Codex 兼容模式的完整供应商配置，明确使用 OpenAI 兼容认证。
function buildCodexCompatibleConfig(baseUrl: string, providerName: string): string {
  return `model_provider = "custom"
model = "${OPENAI_CC_SWITCH_CODEX_MODEL}"
review_model = "${OPENAI_CC_SWITCH_CODEX_REVIEW_MODEL}"
model_reasoning_effort = "${OPENAI_CC_SWITCH_CODEX_REASONING_EFFORT}"
disable_response_storage = true
windows_wsl_setup_acknowledged = true
sandbox_mode = "workspace-write"

[model_providers.custom]
name = ${toTomlString(providerName)}
base_url = ${toTomlString(baseUrl)}
wire_api = "responses"
requires_openai_auth = true

[sandbox_workspace_write]
network_access = true`
}

// 构建 CCS 一键导入链接；OpenAI 平台会携带完整的 Codex 兼容模式 TOML。
export function buildCcSwitchImportDeeplink(input: CcSwitchImportDeeplinkInput): string {
  const config = resolveCcSwitchImportConfig(input.platform, input.clientType, input.baseUrl)
  const codexConfig = input.platform === 'openai'
    ? buildCodexCompatibleConfig(input.baseUrl, input.providerName)
    : null
  const entries: [string, string][] = [
    ['resource', 'provider'],
    ['app', config.app],
    ['name', input.providerName],
    ['homepage', input.baseUrl],
    ['endpoint', config.endpoint],
    ['apiKey', input.apiKey],
    ['configFormat', codexConfig ? 'toml' : 'json'],
    ['usageEnabled', 'true'],
    ['usageScript', encodeBase64Utf8(input.usageScript)],
    ['usageAutoInterval', '30']
  ]

  if (config.model) {
    entries.splice(2, 0, ['model', config.model])
  }
  if (codexConfig) {
    entries.push(['config', encodeBase64Utf8(codexConfig)])
  }

  return `ccswitch://v1/import?${new URLSearchParams(entries).toString()}`
}
