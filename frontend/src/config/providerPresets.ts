// Built-in catalogue of mainstream providers, shown as pick-and-fill cards in
// the create-provider dialog. Selecting a preset fills the name, base URL and
// primary protocol so an admin only needs to paste a key. Everything here is
// public, non-secret configuration; a key is never stored in a preset.
//
// baseUrl points at each provider's OpenAI-compatible endpoint. defaultTestModel
// is only a prefill for the connection test — the dialog can fetch the real
// catalogue for the entered key, so a stale default is harmless. Leave
// defaultTestModel empty for providers that address models by deployment id
// (e.g. Volcengine Ark) rather than a stable public model name.
//
// The display name is not stored here: it is localized via the i18n key
// `providers.preset_<id>` so the card (and the name it fills in) reads in the
// admin's language.

import type { ProtocolId } from '../utils/providerProtocol'

export interface ProviderPreset {
  // id is a stable slug: the card key, the i18n name lookup, and the highlight
  // token. It is not sent to the backend.
  id: string
  baseUrl: string
  protocol: ProtocolId
  // Additional protocols the provider also serves at the same baseUrl, on top
  // of `protocol`. Each is filled in as an endpoint with an empty URL, which
  // means "reuse base_url". Leave undefined for providers that only speak
  // their primary protocol — declaring one they don't serve turns a working
  // cross-protocol translation into an upstream 404.
  extraProtocols?: ProtocolId[]
  defaultTestModel: string
  websiteUrl: string
}

export const PROVIDER_PRESETS: ProviderPreset[] = [
  // YoloRouter Cloud is this project's own hosted service, not a third-party
  // vendor, so it leads the list, its localized name says so explicitly, and
  // the create dialog opens with it preselected. It is an ordinary preset in
  // every other respect: no key ships with it, every field it prefills stays
  // editable, and any other card (including "custom") replaces it in one click.
  {
    id: 'yolorouter',
    baseUrl: 'https://api.yolorouter.com/v1',
    protocol: 'openai',
    // The hosted service runs this same gateway, so it serves all four
    // protocols off one base URL. Declaring the other three keeps native
    // passthrough for Anthropic/Gemini/Responses callers instead of falling
    // back to a lossy cross-protocol translation.
    extraProtocols: ['anthropic', 'gemini', 'responses'],
    defaultTestModel: 'glm-5.2',
    websiteUrl: 'https://yolorouter.com/pricing?utm_source=oss-console&utm_medium=preset',
  },
  {
    id: 'deepseek',
    baseUrl: 'https://api.deepseek.com/v1',
    protocol: 'openai',
    defaultTestModel: 'deepseek-v4-flash',
    websiteUrl: 'https://platform.deepseek.com',
  },
  {
    id: 'moonshot',
    baseUrl: 'https://api.moonshot.cn/v1',
    protocol: 'openai',
    defaultTestModel: 'kimi-latest',
    websiteUrl: 'https://platform.moonshot.cn',
  },
  {
    id: 'zhipu',
    baseUrl: 'https://open.bigmodel.cn/api/paas/v4',
    protocol: 'openai',
    defaultTestModel: 'glm-5.2',
    websiteUrl: 'https://open.bigmodel.cn',
  },
  {
    id: 'zhipu_coding',
    baseUrl: 'https://open.bigmodel.cn/api/coding/paas/v4',
    protocol: 'openai',
    defaultTestModel: 'glm-5.2',
    websiteUrl: 'https://open.bigmodel.cn',
  },
  {
    id: 'qwen',
    baseUrl: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
    protocol: 'openai',
    defaultTestModel: 'qwen-plus',
    websiteUrl: 'https://bailian.console.aliyun.com',
  },
  {
    id: 'siliconflow',
    baseUrl: 'https://api.siliconflow.cn/v1',
    protocol: 'openai',
    defaultTestModel: 'deepseek-ai/DeepSeek-V4-Flash',
    websiteUrl: 'https://siliconflow.cn',
  },
  {
    id: 'minimax',
    baseUrl: 'https://api.minimaxi.com/v1',
    protocol: 'openai',
    defaultTestModel: 'MiniMax-M3',
    websiteUrl: 'https://platform.minimaxi.com',
  },
  {
    id: 'stepfun',
    baseUrl: 'https://api.stepfun.com/v1',
    protocol: 'openai',
    defaultTestModel: 'step-3.7-flash',
    websiteUrl: 'https://platform.stepfun.com',
  },
  {
    id: 'baichuan',
    baseUrl: 'https://api.baichuan-ai.com/v1',
    protocol: 'openai',
    defaultTestModel: '',
    websiteUrl: 'https://platform.baichuan-ai.com',
  },
  {
    id: 'volcengine',
    baseUrl: 'https://ark.cn-beijing.volces.com/api/v3',
    protocol: 'openai',
    defaultTestModel: '',
    websiteUrl: 'https://console.volcengine.com/ark',
  },
]
