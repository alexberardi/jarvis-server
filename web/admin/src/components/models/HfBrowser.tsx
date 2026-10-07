import { useState } from 'react'
import { Search } from 'lucide-react'
import { browseRepo, type HfChoice, type HfRepoResponse, type InstallRequest, type Label } from '@/api/llm'
import { errorMessage } from '@/lib/errors'
import { formatBytes } from '@/lib/format'
import AssignPicker from './AssignPicker'
import FitBadge from './FitBadge'
import HfTokenPrompt from './HfTokenPrompt'
import { fitSummary, isGatedError, normalizeRepo } from './logic'
import { useInstallAction } from './useInstallAction'
import { buttonClass, inputClass } from './styles'
import { Pill, Section } from './ui'

function FileRow({
  repo,
  choice,
  mmprojFiles,
  onNeedsToken,
}: {
  repo: HfRepoResponse
  choice: HfChoice
  mmprojFiles: HfChoice[]
  onNeedsToken: (reason: string) => void
}) {
  const [open, setOpen] = useState(false)
  const [assign, setAssign] = useState<Label[]>([])
  const [mmproj, setMMProj] = useState('')
  const { install, isPending } = useInstallAction(onNeedsToken)
  const installable = choice.kind !== 'mmproj'

  function start() {
    const req: InstallRequest = {
      repo: repo.repo,
      file: choice.file,
      revision: repo.revision,
      kind: choice.kind,
      assign,
    }
    if (mmproj) req.mmproj_file = mmproj
    void install(req, choice.file, () => setOpen(false))
  }

  return (
    <li className="rounded-lg border border-[var(--color-border)] p-3" data-testid={`hf-${choice.file}`}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="break-all font-mono text-xs text-[var(--color-text)]">{choice.file}</span>
            <Pill>{choice.kind}</Pill>
            {choice.quant && <Pill>{choice.quant}</Pill>}
            {choice.shards.length > 1 && <Pill>{choice.shards.length} parts</Pill>}
            <FitBadge fit={choice.fit} />
          </div>
          <p className="text-xs text-[var(--color-text-muted)]">
            {formatBytes(choice.size)} · {fitSummary(choice.fit)}
          </p>
        </div>
        {installable && (
          <button type="button" className={buttonClass.secondary} onClick={() => setOpen(!open)} aria-expanded={open}>
            {open ? 'Close' : 'Install'}
          </button>
        )}
      </div>
      {open && (
        <div className="mt-3 space-y-2 border-t border-[var(--color-border)] pt-3">
          <AssignPicker kind={choice.kind} value={assign} onChange={setAssign} idPrefix={`hf-${choice.file}`} />
          {choice.kind === 'llm' && mmprojFiles.length > 0 && (
            <label className="flex flex-wrap items-center gap-2 text-xs text-[var(--color-text)]">
              Vision projector:
              <select
                value={mmproj}
                onChange={(e) => setMMProj(e.target.value)}
                className="rounded border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-2 py-1 text-xs"
              >
                <option value="">none</option>
                {mmprojFiles.map((m) => (
                  <option key={m.file} value={m.file}>
                    {m.file} ({formatBytes(m.size)})
                  </option>
                ))}
              </select>
            </label>
          )}
          <div className="flex justify-end">
            <button type="button" className={buttonClass.primary} disabled={isPending} onClick={start}>
              {isPending ? 'Starting…' : 'Start install'}
            </button>
          </div>
        </div>
      )}
    </li>
  )
}

/**
 * HfBrowser installs any GGUF (or whisper .bin) from a pasted Hugging Face repo. Gated repos ask
 * for `llm.hf_token`.
 */
export default function HfBrowser() {
  const [input, setInput] = useState('')
  const [result, setResult] = useState<HfRepoResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [tokenReason, setTokenReason] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  async function browse() {
    const repo = normalizeRepo(input)
    if (!repo.includes('/')) {
      setError('Enter a repo as owner/name, e.g. Qwen/Qwen2.5-0.5B-Instruct-GGUF')
      return
    }
    setLoading(true)
    setError(null)
    setResult(null)
    try {
      const res = await browseRepo(repo)
      setResult(res)
      setTokenReason(null)
    } catch (err) {
      if (isGatedError(err)) setTokenReason(errorMessage(err))
      setError(errorMessage(err, 'Could not read that repo'))
    } finally {
      setLoading(false)
    }
  }

  const mmprojFiles = result?.files.filter((f) => f.kind === 'mmproj') ?? []

  return (
    <Section
      title="Hugging Face"
      icon={Search}
      description="Paste a repo to install a model that isn't in the catalog. Only GGUF and whisper files are listed."
    >
      <div className="space-y-3">
        <form
          className="flex gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            void browse()
          }}
        >
          <label htmlFor="hf-repo" className="sr-only">
            Hugging Face repo
          </label>
          <input
            id="hf-repo"
            value={input}
            onChange={(e) => setInput(e.target.value)}
            placeholder="owner/name or https://huggingface.co/owner/name"
            className={inputClass}
          />
          <button type="submit" className={buttonClass.primary} disabled={loading || !input.trim()}>
            {loading ? 'Reading…' : 'Browse'}
          </button>
        </form>

        {error && <p className="text-sm text-red-500">{error}</p>}
        {tokenReason && <HfTokenPrompt reason="This repo is gated or private." onSaved={() => void browse()} />}

        {result && (
          <div className="space-y-2">
            <p className="text-xs text-[var(--color-text-muted)]">
              <span className="font-mono">{result.repo}</span> at <span className="font-mono">{result.revision.slice(0, 12)}</span>
              {result.gated && ' · gated'} · {result.files.length} installable file{result.files.length === 1 ? '' : 's'}
            </p>
            {result.files.length === 0 ? (
              <p className="text-sm text-[var(--color-text-muted)]">No GGUF or whisper files in this repo.</p>
            ) : (
              <ul className="space-y-2">
                {result.files.map((f) => (
                  <FileRow
                    key={f.file}
                    repo={result}
                    choice={f}
                    mmprojFiles={mmprojFiles}
                    onNeedsToken={(r) => setTokenReason(r)}
                  />
                ))}
              </ul>
            )}
          </div>
        )}

        {!tokenReason && (
          <details className="text-xs text-[var(--color-text-muted)]">
            <summary className="cursor-pointer">Hugging Face token for gated repos</summary>
            <div className="mt-2">
              <HfTokenPrompt compact />
            </div>
          </details>
        )}
      </div>
    </Section>
  )
}
