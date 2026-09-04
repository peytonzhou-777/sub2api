import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(
  resolve(process.cwd(), 'src/components/admin/account/ReAuthAccountModal.vue'),
  'utf8'
)

describe('ReAuthAccountModal OpenAI AccountPersona re-auth', () => {
  it('routes account re-auth through the protected primary Persona endpoints', () => {
    expect(source).toContain('generateOpenAIPrimaryPersonaAuthUrl(props.account.id)')
    expect(source).toContain('exchangeOpenAIPrimaryPersonaCode(')
    expect(source).toContain('generateOpenAIAccountPersonaAuthUrl(')
    expect(source).toContain('exchangeOpenAIAccountPersonaCode(')
  })

  it('does not send primary OAuth runtime tokens to the legacy account credentials endpoint', () => {
    const openAIStart = source.indexOf('if (isOpenAILike.value) {', source.indexOf('const handleExchangeCode'))
    const nextPlatform = source.indexOf('} else if (isGemini.value)', openAIStart)
    expect(openAIStart).toBeGreaterThan(-1)
    expect(nextPlatform).toBeGreaterThan(openAIStart)
    expect(source.slice(openAIStart, nextPlatform)).not.toContain('applyOAuthCredentials')
  })
})
