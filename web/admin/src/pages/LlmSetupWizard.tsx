import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { toast } from 'sonner'
import { Brain, Download, Settings, CheckCircle, AlertTriangle, Loader2, Lock } from 'lucide-react'
import { cn } from '@/lib/utils'
import { LLM_MODELS, type LlmModel } from '@/data/models'
import { useConfigureLlm, useDownloadModel } from '@/hooks/useLlmSetup'

type WizardStep = 'select' | 'token' | 'download' | 'configure' | 'done'

export default function LlmSetupWizard() {
  const navigate = useNavigate()
  const configureMutation = useConfigureLlm()
  const downloadMutation = useDownloadModel()

  const [step, setStep] = useState<WizardStep>('select')
  const [selectedModel, setSelectedModel] = useState<LlmModel | null>(null)
  const [backend, setBackend] = useState<'GGUF' | 'VLLM'>('GGUF')
  const [hfToken, setHfToken] = useState('')
  const [error, setError] = useState<string | null>(null)

  const handleModelSelect = (model: LlmModel) => {
    setSelectedModel(model)
    if (model.gated) {
      setStep('token')
    } else {
      handleDownload(model, '')
    }
  }

  const handleTokenSubmit = () => {
    if (!hfToken.trim()) {
      setError('HuggingFace token is required for gated models')
      return
    }
    setError(null)
    if (selectedModel) {
      handleDownload(selectedModel, hfToken)
    }
  }

  const handleDownload = async (model: LlmModel, token: string) => {
    setStep('download')
    setError(null)

    const repo = backend === 'GGUF' ? model.hfRepoGguf : model.hfRepoVllm
    const filename = backend === 'GGUF' ? model.ggufFilename : undefined

    try {
      await downloadMutation.mutateAsync({ repo, filename, token: token || undefined })
      handleConfigure(model)
    } catch (err) {
      setError(`Download failed: ${(err as Error).message}`)
      setStep('select')
    }
  }

  const handleConfigure = async (model: LlmModel) => {
    setStep('configure')
    setError(null)

    const modelPath = backend === 'GGUF'
      ? `.models/${model.ggufFilename}`
      : `.models/${model.hfRepoVllm.split('/').pop()}`

    const settings: Record<string, unknown> = {
      'model.live.name': modelPath,
      'model.live.backend': backend,
      'model.live.chat_format': model.chatFormat,
      'model.live.context_window': model.contextWindow,
    }

    if (backend === 'GGUF') {
      settings['inference.gguf.n_gpu_layers'] = -1
    }

    if (backend === 'VLLM' && model.quantization) {
      settings['inference.vllm.quantization'] = model.quantization
    }

    try {
      await configureMutation.mutateAsync(settings)
      setStep('done')
      toast.success('LLM configured successfully')
    } catch (err) {
      setError(`Configuration failed: ${(err as Error).message}`)
      setStep('select')
    }
  }

  const handleSkip = () => {
    navigate('/dashboard')
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-[var(--color-bg)] p-4">
      <div className="w-full max-w-2xl rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-8 shadow-lg">
        <div className="mb-8 text-center">
          <Brain className="mx-auto mb-3 text-[var(--color-primary)]" size={40} />
          <h1 className="text-2xl font-bold text-[var(--color-text)]">LLM Setup</h1>
          <p className="mt-1 text-sm text-[var(--color-text-muted)]">
            Configure a local language model for Jarvis
          </p>
        </div>

        {/* Progress indicator */}
        <div className="mb-8 flex items-center justify-center gap-2">
          {(['select', 'download', 'configure', 'done'] as const).map((s, i) => {
            // Map 'token' step to 'select' for progress display
            const displayStep = step === 'token' ? 'select' : step
            const steps = ['select', 'download', 'configure', 'done']
            return (
              <div key={s} className="flex items-center gap-2">
                <div
                  className={cn(
                    'h-2 w-2 rounded-full',
                    displayStep === s ? 'bg-[var(--color-primary)]' : (
                      steps.indexOf(displayStep) > i
                        ? 'bg-green-500'
                        : 'bg-[var(--color-border)]'
                    ),
                  )}
                />
                {i < 3 && <div className="h-px w-8 bg-[var(--color-border)]" />}
              </div>
            )
          })}
        </div>

        {error && (
          <div className="mb-6 flex items-start gap-2 rounded-lg border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-500">
            <AlertTriangle size={16} className="mt-0.5 shrink-0" />
            {error}
          </div>
        )}

        {/* Step: Select Model */}
        {step === 'select' && (
          <div className="space-y-4">
            <div className="mb-4 flex items-center justify-between">
              <h2 className="text-sm font-semibold text-[var(--color-text)]">Select a model</h2>
              <div className="flex gap-2">
                <button
                  onClick={() => setBackend('GGUF')}
                  className={cn(
                    'rounded-md px-3 py-1 text-xs font-medium transition-colors',
                    backend === 'GGUF'
                      ? 'bg-[var(--color-primary)] text-white'
                      : 'bg-[var(--color-surface-alt)] text-[var(--color-text-muted)] hover:text-[var(--color-text)]',
                  )}
                >
                  GGUF
                </button>
                <button
                  onClick={() => setBackend('VLLM')}
                  className={cn(
                    'rounded-md px-3 py-1 text-xs font-medium transition-colors',
                    backend === 'VLLM'
                      ? 'bg-[var(--color-primary)] text-white'
                      : 'bg-[var(--color-surface-alt)] text-[var(--color-text-muted)] hover:text-[var(--color-text)]',
                  )}
                >
                  vLLM
                </button>
              </div>
            </div>

            <div className="grid gap-3">
              {LLM_MODELS.map((model) => (
                <button
                  key={model.id}
                  onClick={() => handleModelSelect(model)}
                  className={cn(
                    'flex items-center justify-between rounded-lg border border-[var(--color-border)] p-4 text-left transition-colors',
                    'hover:border-[var(--color-primary)] hover:bg-[var(--color-surface-alt)]',
                  )}
                >
                  <div>
                    <div className="flex items-center gap-2">
                      <span className="font-medium text-[var(--color-text)]">{model.displayName}</span>
                      {model.gated && (
                        <span className="flex items-center gap-1 rounded bg-yellow-500/15 px-1.5 py-0.5 text-[10px] font-medium text-yellow-600 dark:text-yellow-400">
                          <Lock size={10} />
                          Gated
                        </span>
                      )}
                    </div>
                    <p className="mt-0.5 text-xs text-[var(--color-text-muted)]">
                      {model.quantization ? `${model.quantization.toUpperCase()} 4-bit` : model.chatFormat} &middot; {model.contextWindow.toLocaleString()} ctx
                    </p>
                  </div>
                  <span className="text-sm text-[var(--color-text-muted)]">
                    ~{backend === 'GGUF' ? model.sizeGguf : model.sizeVllm}
                  </span>
                </button>
              ))}
            </div>

            <div className="flex justify-end pt-2">
              <button
                onClick={handleSkip}
                className="text-sm text-[var(--color-text-muted)] hover:text-[var(--color-text)]"
              >
                Skip for now
              </button>
            </div>
          </div>
        )}

        {/* Step: HF Token */}
        {step === 'token' && selectedModel && (
          <div className="space-y-4">
            <h2 className="text-sm font-semibold text-[var(--color-text)]">
              HuggingFace Access Required
            </h2>
            <p className="text-sm text-[var(--color-text-muted)]">
              <strong>{selectedModel.displayName}</strong> is a gated model. You need to:
            </p>
            <ol className="list-inside list-decimal space-y-1 text-sm text-[var(--color-text-muted)]">
              <li>Visit the model page on huggingface.co and accept the license agreement</li>
              <li>Create an access token at huggingface.co/settings/tokens</li>
              <li>Paste your token below</li>
            </ol>

            <input
              type="password"
              value={hfToken}
              onChange={(e) => setHfToken(e.target.value)}
              placeholder="hf_..."
              className={cn(
                'w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2',
                'text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]',
                'outline-none focus:ring-2 focus:ring-[var(--color-primary)]',
              )}
            />

            <div className="flex justify-between">
              <button
                onClick={() => { setStep('select'); setError(null) }}
                className="text-sm text-[var(--color-text-muted)] hover:text-[var(--color-text)]"
              >
                Back
              </button>
              <button
                onClick={handleTokenSubmit}
                className="rounded-lg bg-[var(--color-primary)] px-4 py-2 text-sm text-white hover:opacity-90"
              >
                Continue
              </button>
            </div>
          </div>
        )}

        {/* Step: Downloading */}
        {step === 'download' && (
          <div className="flex flex-col items-center py-8">
            <Download className="mb-4 animate-bounce text-[var(--color-primary)]" size={32} />
            <h2 className="text-lg font-semibold text-[var(--color-text)]">Downloading model...</h2>
            <p className="mt-1 text-sm text-[var(--color-text-muted)]">
              {selectedModel?.displayName}
              {' '}(~{backend === 'GGUF' ? selectedModel?.sizeGguf : selectedModel?.sizeVllm})
            </p>
            <p className="mt-4 text-xs text-[var(--color-text-muted)]">
              This may take several minutes depending on your connection
            </p>
          </div>
        )}

        {/* Step: Configuring */}
        {step === 'configure' && (
          <div className="flex flex-col items-center py-8">
            <Settings className="mb-4 animate-spin text-[var(--color-primary)]" size={32} />
            <h2 className="text-lg font-semibold text-[var(--color-text)]">Configuring...</h2>
            <p className="mt-1 text-sm text-[var(--color-text-muted)]">
              Writing settings and restarting LLM proxy
            </p>
          </div>
        )}

        {/* Step: Done */}
        {step === 'done' && (
          <div className="flex flex-col items-center py-8">
            <CheckCircle className="mb-4 text-green-500" size={40} />
            <h2 className="text-lg font-semibold text-[var(--color-text)]">LLM Ready</h2>
            <p className="mt-1 text-sm text-[var(--color-text-muted)]">
              {selectedModel?.displayName} configured on {backend}
            </p>
            <p className="mt-1 text-xs text-[var(--color-text-muted)]">
              The model service is restarting. It may take a minute to load the model.
            </p>
            <button
              onClick={() => navigate('/dashboard')}
              className="mt-6 rounded-lg bg-[var(--color-primary)] px-6 py-2 text-sm text-white hover:opacity-90"
            >
              Go to Dashboard
            </button>
          </div>
        )}

        {/* Loading overlay for mutations */}
        {(downloadMutation.isPending || configureMutation.isPending) && step !== 'download' && step !== 'configure' && (
          <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
            <Loader2 className="animate-spin text-white" size={32} />
          </div>
        )}
      </div>
    </div>
  )
}
