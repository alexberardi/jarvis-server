export interface LlmModel {
  id: string
  displayName: string
  hfRepoVllm: string
  hfRepoGguf: string
  ggufFilename: string
  chatFormat: string
  contextWindow: number
  sizeVllm: string
  sizeGguf: string
  vramMb: number
  gated: boolean
  quantization: string
  /** Prompt provider name for command-center (llm.interface setting) */
  promptProvider: string
}

export const LLM_MODELS: LlmModel[] = [
  {
    id: 'qwen3-4b',
    displayName: 'Qwen 3 4B',
    hfRepoVllm: 'Qwen/Qwen3-4B-AWQ',
    hfRepoGguf: 'Qwen/Qwen3-4B-GGUF',
    ggufFilename: 'Qwen3-4B-Q4_K_M.gguf',
    chatFormat: 'chatml',
    contextWindow: 40960,
    sizeVllm: '2.5GB',
    sizeGguf: '2.5GB',
    vramMb: 4096,
    gated: false,
    quantization: 'awq',
    promptProvider: 'Qwen3_8B_Compressed',
  },
  {
    id: 'qwen3-8b',
    displayName: 'Qwen 3 8B',
    hfRepoVllm: 'Qwen/Qwen3-8B-AWQ',
    hfRepoGguf: 'Qwen/Qwen3-8B-GGUF',
    ggufFilename: 'Qwen3-8B-Q4_K_M.gguf',
    chatFormat: 'chatml',
    contextWindow: 40960,
    sizeVllm: '5.7GB',
    sizeGguf: '5GB',
    vramMb: 7168,
    gated: false,
    quantization: 'awq',
    promptProvider: 'Qwen3_8B_Compressed',
  },
  {
    id: 'qwen3-14b',
    displayName: 'Qwen 3 14B',
    hfRepoVllm: 'Qwen/Qwen3-14B-AWQ',
    hfRepoGguf: 'Qwen/Qwen3-14B-GGUF',
    ggufFilename: 'Qwen3-14B-Q4_K_M.gguf',
    chatFormat: 'chatml',
    contextWindow: 40960,
    sizeVllm: '9.4GB',
    sizeGguf: '9GB',
    vramMb: 12288,
    gated: false,
    quantization: 'awq',
    promptProvider: 'Qwen3LargeUntrained',
  },
  {
    id: 'qwen25-7b',
    displayName: 'Qwen 2.5 7B Instruct',
    hfRepoVllm: 'Qwen/Qwen2.5-7B-Instruct-AWQ',
    hfRepoGguf: 'Qwen/Qwen2.5-7B-Instruct-GGUF',
    ggufFilename: 'qwen2.5-7b-instruct-q4_k_m.gguf',
    chatFormat: 'chatml',
    contextWindow: 32768,
    sizeVllm: '4.7GB',
    sizeGguf: '4.7GB',
    vramMb: 6144,
    gated: false,
    quantization: 'awq',
    promptProvider: 'Qwen25MediumUntrained',
  },
  {
    id: 'llama-3.1-8b',
    displayName: 'Llama 3.1 8B Instruct',
    hfRepoVllm: 'hugging-quants/Meta-Llama-3.1-8B-Instruct-AWQ-INT4',
    hfRepoGguf: 'lmstudio-community/Meta-Llama-3.1-8B-Instruct-GGUF',
    ggufFilename: 'Meta-Llama-3.1-8B-Instruct-Q4_K_M.gguf',
    chatFormat: 'llama-3',
    contextWindow: 131072,
    sizeVllm: '5.4GB',
    sizeGguf: '5GB',
    vramMb: 7168,
    gated: false,
    quantization: 'awq',
    promptProvider: 'Llama31MediumUntrained',
  },
  {
    id: 'hermes-3-8b',
    displayName: 'Hermes 3 Llama 3.1 8B',
    hfRepoVllm: 'solidrust/Hermes-3-Llama-3.1-8B-AWQ',
    hfRepoGguf: 'NousResearch/Hermes-3-Llama-3.1-8B-GGUF',
    ggufFilename: 'Hermes-3-Llama-3.1-8B.Q4_K_M.gguf',
    chatFormat: 'chatml',
    contextWindow: 131072,
    sizeVllm: '5.4GB',
    sizeGguf: '4.9GB',
    vramMb: 7168,
    gated: false,
    quantization: 'awq',
    promptProvider: 'HermesMediumUntrained',
  },
]
