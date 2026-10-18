// Writes <E2E_OUT>/results.json: one record per test, in the shape scripts/e2e merges into the
// run's report.json (docs/dev/e2e.md has the schema). Paths are relative to the run directory.
import type { FullResult, Reporter, TestCase, TestResult } from '@playwright/test/reporter'
import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { outDir } from './helpers/env'

interface Record {
  suite: 'web'
  id: string
  title: string
  file: string
  line: number
  status: TestResult['status']
  duration_ms: number
  error: string | null
  screenshots: string[]
  trace: string | null
  console_errors: string[]
}

// Playwright's error text carries terminal colours.
const plain = (s: string) => s.replace(/\u001b\[[0-9;]*m/g, '')

export default class ResultsReporter implements Reporter {
  private records: Record[] = []
  private dir = outDir()
  private runDir = path.dirname(this.dir)

  onTestEnd(test: TestCase, result: TestResult) {
    const rel = (p: string) => path.relative(this.runDir, p)
    const att = (name: string) => result.attachments.filter((a) => a.name === name)
    const consoleBody = att('console-errors')[0]?.body
    this.records.push({
      suite: 'web',
      id: test.id,
      title: test.titlePath().slice(2).join(' › '),
      file: path.relative(path.resolve(import.meta.dirname), test.location.file),
      line: test.location.line,
      status: result.status,
      duration_ms: result.duration,
      error: result.errors.length ? plain(result.errors.map((e) => e.message ?? String(e.value)).join('\n\n')) : null,
      screenshots: att('screenshot').flatMap((a) => (a.path ? [rel(a.path)] : [])),
      trace: att('trace').find((a) => a.path)?.path ? rel(att('trace').find((a) => a.path)!.path!) : null,
      console_errors: consoleBody ? consoleBody.toString('utf8').split('\n') : [],
    })
  }

  onEnd(result: FullResult) {
    mkdirSync(this.dir, { recursive: true })
    writeFileSync(
      path.join(this.dir, 'results.json'),
      JSON.stringify({ suite: 'web', status: result.status, duration_ms: result.duration, tests: this.records }, null, 2) + '\n',
    )
  }
}
