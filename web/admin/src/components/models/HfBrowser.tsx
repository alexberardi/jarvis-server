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
import { Badge, Button, Input, Select } from '@/components/ui'
import ModelCard from './ModelCard'

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
  const [provider, setProvider] = useState('')
  const { install, isPending } = useInstallAction(onNeedsToken)
  const folder = !!choice.directory
  const installable = choice.kind !== 'mmproj' && !choice.unavailable
  // A folder whose name and config.json don't say which prompt provider it needs: the user picks.
  const needsProvider = folder && !choice.prompt_provider
  const name = folder ? repo.repo : choice.file

  function start() {
    const req: InstallRequest = {
      repo: repo.repo,
      file: choice.file,
      revision: repo.revision,
      kind: choice.kind,
      assign,
    }
    if (mmproj && !folder) req.mmproj_file = mmproj
    if (needsProvider) req.prompt_provider = provider
    void install(req, name, () => setOpen(false))
  }

  return (
    <li className="py-3 first:pt-0 last:pb-0" data-testid={`hf-${choice.file}`}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-small break-all text-text">{folder ? repo.repo : choice.file}</span>
            {folder ? <Badge>Model folder</Badge> : <Badge>{choice.kind}</Badge>}
            {choice.quant && <Badge>{choice.quant}</Badge>}
            {folder ? (
              <>
                <Badge>{choice.shards.length} files</Badge>
                {choice.engine && <Badge title="The engine that runs this folder here">{choice.engine}</Badge>}
                <Badge title="This engine reads text only: vision is off for this model">text-only</Badge>
              </>
            ) : (
              choice.shards.length > 1 && <Badge>{choice.shards.length} parts</Badge>
            )}
            <FitBadge fit={choice.fit} />
          </div>
          <p className="text-small text-text-muted">
            {formatBytes(choice.size)} · {fitSummary(choice.fit)}
          </p>
          {choice.unavailable && <p className="text-small text-warning">{choice.unavailable}</p>}
        </div>
        {installable && (
          <Button size="sm" onClick={() => setOpen(!open)} aria-expanded={open}>
            {open ? 'Close' : 'Install'}
          </Button>
        )}
      </div>
      {open && (
        <div className="mt-3 space-y-3 rounded-control bg-surface-alt p-3">
          <AssignPicker kind={choice.kind} value={assign} onChange={setAssign} idPrefix={`hf-${choice.file}`} />
          {folder &&
            (needsProvider ? (
              <Select
                label="Prompt provider (this folder doesn't say which one it needs)"
                wrapperClassName="max-w-md"
                value={provider}
                onValueChange={setProvider}
                options={[
                  { value: '', label: 'pick one' },
                  ...(repo.prompt_providers ?? []).map((p) => ({ value: p, label: p })),
                ]}
              />
            ) : (
              <p className="text-small text-text-muted">
                Prompt provider: {choice.prompt_provider} (from the folder's name and config.json)
              </p>
            ))}
          {!folder && choice.kind === 'llm' && mmprojFiles.length > 0 && (
            <Select
              label="Vision projector"
              wrapperClassName="max-w-md"
              value={mmproj}
              onValueChange={setMMProj}
              options={[
                { value: '', label: 'none' },
                ...mmprojFiles.map((m) => ({ value: m.file, label: `${m.file} (${formatBytes(m.size)})` })),
              ]}
            />
          )}
          <div className="flex justify-end">
            <Button variant="primary" size="sm" loading={isPending} disabled={needsProvider && !provider} onClick={start}>
              Start install
            </Button>
          </div>
        </div>
      )}
    </li>
  )
}

/**
 * HfBrowser installs any GGUF (or whisper .bin), or a Hugging Face model folder (safetensors, for
 * MLX on Apple Silicon and vLLM or Transformers elsewhere), from a pasted repo. Gated repos ask
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
    <ModelCard
      title="Hugging Face"
      icon={Search}
      description="Paste a repo to install a model that isn't in the catalog: its GGUF and whisper files, or its safetensors model folder (MLX on Apple Silicon, vLLM or Transformers elsewhere)."
    >
      <div className="space-y-3">
        <form
          className="flex flex-wrap gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            void browse()
          }}
        >
          <Input
            id="hf-repo"
            aria-label="Hugging Face repo"
            wrapperClassName="min-w-0 flex-1 basis-60"
            value={input}
            onChange={(e) => setInput(e.target.value)}
            placeholder="owner/name or https://huggingface.co/owner/name"
          />
          <Button type="submit" variant="primary" disabled={!input.trim()} loading={loading}>
            Browse
          </Button>
        </form>

        {error && (
          <p role="alert" className="text-small text-danger">
            {error}
          </p>
        )}
        {tokenReason && <HfTokenPrompt reason="This repo is gated or private." onSaved={() => void browse()} />}

        {result && (
          <div className="space-y-2">
            <p className="text-small text-text-muted">
              <span className="font-mono">{result.repo}</span> at <span className="font-mono">{result.revision.slice(0, 12)}</span>
              {result.gated && ' · gated'} · {result.files.length} installable model{result.files.length === 1 ? '' : 's'}
            </p>
            {result.files.length === 0 ? (
              <p className="text-small text-text-muted">No GGUF, whisper or safetensors model files in this repo.</p>
            ) : (
              <ul className="divide-y divide-border">
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
          <details className="text-small text-text-muted">
            <summary className="min-h-10 cursor-pointer content-center rounded-control hover:text-text">
              Hugging Face token for gated repos
            </summary>
            <div className="mt-2">
              <HfTokenPrompt compact />
            </div>
          </details>
        )}
      </div>
    </ModelCard>
  )
}
