import { useState } from 'react'
import { Download, Trash2, HardDrive, Sparkles, Key } from 'lucide-react'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'
import { useInstalledModels, useDownloadModel, useDeleteModel } from '@/hooks/useModels'
import { LLM_MODELS } from '@/data/models'

export default function ModelsPage() {
  const [hfToken, setHfToken] = useState('')
  const [customRepo, setCustomRepo] = useState('')
  const [customFilename, setCustomFilename] = useState('')
  const [downloadingKey, setDownloadingKey] = useState<string | null>(null)

  const { data: installed, isLoading: loadingInstalled } = useInstalledModels()
  const downloadMutation = useDownloadModel()
  const deleteMutation = useDeleteModel()

  function handleDownload(repo: string, filename?: string) {
    const key = `${repo}/${filename ?? ''}`
    setDownloadingKey(key)
    downloadMutation.mutate(
      { repo, filename: filename || undefined, token: hfToken || undefined },
      {
        onSuccess: (res) => { toast.success(res.message); setDownloadingKey(null) },
        onError: (err) => { toast.error(err instanceof Error ? err.message : 'Download failed'); setDownloadingKey(null) },
      },
    )
  }

  function handleDelete(name: string) {
    if (!confirm(`Delete ${name}?`)) return
    deleteMutation.mutate(name, {
      onSuccess: () => toast.success(`Deleted ${name}`),
      onError: () => toast.error('Failed to delete'),
    })
  }

  function handleCustomDownload() {
    if (!customRepo) return
    handleDownload(customRepo, customFilename)
  }

  return (
    <div className="mx-auto max-w-4xl space-y-8 p-6">
      <div>
        <h1 className="text-2xl font-bold text-[var(--color-text)]">Models</h1>
        <p className="text-sm text-[var(--color-text-muted)]">
          Manage LLM models on the inference server
        </p>
      </div>

      {/* HuggingFace Token */}
      <section className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
        <div className="flex items-center gap-2 mb-3">
          <Key size={16} className="text-[var(--color-text-muted)]" />
          <h2 className="text-sm font-semibold text-[var(--color-text)]">HuggingFace Token</h2>
          <span className="text-xs text-[var(--color-text-muted)]">(optional, for gated models)</span>
        </div>
        <input
          type="password"
          value={hfToken}
          onChange={(e) => setHfToken(e.target.value)}
          placeholder="hf_..."
          className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2 text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]"
        />
      </section>

      {/* Installed Models */}
      <section className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
        <div className="flex items-center gap-2 mb-4">
          <HardDrive size={16} className="text-[var(--color-text-muted)]" />
          <h2 className="text-sm font-semibold text-[var(--color-text)]">Installed Models</h2>
        </div>
        {loadingInstalled ? (
          <p className="text-sm text-[var(--color-text-muted)]">Loading...</p>
        ) : !installed?.length ? (
          <p className="text-sm text-[var(--color-text-muted)]">No models installed yet</p>
        ) : (
          <div className="space-y-2">
            {installed.map((model) => (
              <div
                key={model.name}
                className="flex items-center justify-between rounded-md border border-[var(--color-border)] px-3 py-2"
              >
                <div>
                  <span className="text-sm font-medium text-[var(--color-text)]">{model.name}</span>
                  <span className="ml-2 text-xs text-[var(--color-text-muted)]">{model.sizeFormatted}</span>
                </div>
                <button
                  onClick={() => handleDelete(model.name)}
                  disabled={deleteMutation.isPending}
                  className="rounded p-1 text-[var(--color-text-muted)] hover:bg-red-500/10 hover:text-red-500"
                >
                  <Trash2 size={14} />
                </button>
              </div>
            ))}
          </div>
        )}
      </section>

      {/* Suggested Models */}
      <section className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
        <div className="flex items-center gap-2 mb-4">
          <Sparkles size={16} className="text-[var(--color-text-muted)]" />
          <h2 className="text-sm font-semibold text-[var(--color-text)]">Suggested Models</h2>
        </div>
        <div className="space-y-3">
          {LLM_MODELS.map((model) => {
            const isInstalled = installed?.some(
              (m) => m.name === model.ggufFilename || m.name.includes(model.ggufFilename),
            )
            return (
              <div
                key={model.id}
                className="flex items-center justify-between rounded-md border border-[var(--color-border)] px-4 py-3"
              >
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="text-sm font-medium text-[var(--color-text)]">{model.displayName}</span>
                    <span className="rounded bg-[var(--color-surface-alt)] px-1.5 py-0.5 text-xs text-[var(--color-text-muted)]">
                      ~{model.sizeGguf}
                    </span>
                    {isInstalled && (
                      <span className="rounded bg-green-500/10 px-1.5 py-0.5 text-xs text-green-500">
                        installed
                      </span>
                    )}
                  </div>
                  <p className="text-xs text-[var(--color-text-muted)]">
                    {model.hfRepoGguf} / {model.ggufFilename}
                  </p>
                  <p className="text-xs text-[var(--color-text-muted)]">
                    Prompt provider: <code className="text-[var(--color-primary)]">{model.promptProvider}</code>
                  </p>
                </div>
                <button
                  onClick={() => handleDownload(model.hfRepoGguf, model.ggufFilename)}
                  disabled={downloadingKey !== null}
                  className={cn(
                    'ml-4 flex items-center gap-1 rounded-md px-3 py-1.5 text-sm font-medium transition-colors',
                    isInstalled
                      ? 'border border-[var(--color-border)] text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)]'
                      : 'bg-[var(--color-primary)] text-white hover:opacity-90',
                  )}
                >
                  <Download size={14} />
                  {downloadingKey === `${model.hfRepoGguf}/${model.ggufFilename}` ? 'Downloading...' : isInstalled ? 'Re-download' : 'Download'}
                </button>
              </div>
            )
          })}
        </div>
      </section>

      {/* Custom Download */}
      <section className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
        <div className="flex items-center gap-2 mb-4">
          <Download size={16} className="text-[var(--color-text-muted)]" />
          <h2 className="text-sm font-semibold text-[var(--color-text)]">Download from HuggingFace</h2>
        </div>
        <div className="space-y-3">
          <div>
            <label className="mb-1 block text-xs text-[var(--color-text-muted)]">
              Repository (e.g. Qwen/Qwen3-14B-GGUF)
            </label>
            <input
              value={customRepo}
              onChange={(e) => setCustomRepo(e.target.value)}
              placeholder="owner/repo-name"
              className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2 text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]"
            />
          </div>
          <div>
            <label className="mb-1 block text-xs text-[var(--color-text-muted)]">
              Filename (optional — downloads specific file instead of full repo)
            </label>
            <input
              value={customFilename}
              onChange={(e) => setCustomFilename(e.target.value)}
              placeholder="model-name.gguf"
              className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2 text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]"
            />
          </div>
          <button
            onClick={handleCustomDownload}
            disabled={!customRepo || downloadingKey !== null}
            className="flex items-center gap-2 rounded-md bg-[var(--color-primary)] px-4 py-2 text-sm font-medium text-white transition-colors hover:opacity-90 disabled:opacity-50"
          >
            <Download size={14} />
            {downloadingKey === `${customRepo}/${customFilename}` ? 'Downloading...' : 'Download'}
          </button>
        </div>
      </section>
    </div>
  )
}
