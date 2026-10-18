<script lang="ts">
  // Provider status: one table, one row per provider.
  //
  // The status column is rendered straight from the server and never computed
  // here — the client's copy of "configured" would be a claim, and the spec
  // is explicit that only a real completion earns "live-verified". The Test
  // button calls the server's smoke action, which is the only thing that can
  // set that status; this view just reports what came back.
  import { onMount } from 'svelte'
  import { api, type ProviderStatusRow, type ProviderStatusState } from '../api/client'
  import Badge from '../lib/ui/Badge.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Card from '../lib/ui/Card.svelte'

  let rows = $state<ProviderStatusRow[]>([])
  let loading = $state(true)
  let loadError = $state('')
  let busy = $state<Record<string, 'validate' | 'smoke' | undefined>>({})
  let messages = $state<Record<string, string>>({})

  const stateTone: Record<ProviderStatusState, 'danger' | 'warning' | 'success'> = {
    unavailable: 'danger',
    configured: 'warning',
    'live-verified': 'success',
  }

  async function load() {
    loading = true
    loadError = ''
    try {
      rows = (await api.getProviderStatus()).providers || []
    } catch (e) {
      loadError = e instanceof Error ? e.message : String(e)
    } finally {
      loading = false
    }
  }

  // Run one of the two server actions and refresh the row from the response
  // rather than guessing locally what it should now say.
  async function run(id: string, action: 'validate' | 'smoke') {
    busy = { ...busy, [id]: action }
    const next = { ...messages }
    delete next[id]
    messages = next
    try {
      if (action === 'validate') {
        const res = await api.checkProviderKey(id)
        if (res.message) {
          messages = { ...messages, [id]: res.message }
        }
      } else {
        const res = await api.smokeProvider(id)
        if (res.message) {
          messages = { ...messages, [id]: res.message }
        }
      }
      await load()
    } catch (e) {
      messages = { ...messages, [id]: e instanceof Error ? e.message : String(e) }
    } finally {
      const b = { ...busy }
      delete b[id]
      busy = b
    }
  }

  function verifiedAtLabel(iso: string | null): string {
    if (!iso) return '—'
    const d = new Date(iso)
    if (Number.isNaN(d.getTime())) return iso
    return d.toLocaleString()
  }

  // A provider whose docs name no models endpoint still deserves a row — the
  // honest answer is "unavailable", and hiding it would hide the gap.
  function canValidate(row: ProviderStatusRow): boolean {
    return row.models_endpoint !== null
  }

  function canSmoke(row: ProviderStatusRow): boolean {
    return row.chat_endpoint !== null && row.chat_endpoint !== undefined
  }

  onMount(load)
</script>

<Card>
  <div class="flex flex-wrap items-center justify-between gap-3">
    <div>
      <h2 class="text-base font-semibold text-text-main">Provider status</h2>
      <p class="text-xs text-text-muted">
        A row turns green only after one real completion returns text. A model list alone marks
        it configured.
      </p>
    </div>
    <Button variant="secondary" onclick={load} disabled={loading}>
      {loading ? 'Refreshing…' : 'Refresh'}
    </Button>
  </div>

  {#if loadError}
    <p class="mt-3 text-sm text-red-500">{loadError}</p>
  {/if}

  <div class="mt-4 overflow-x-auto">
    <table class="w-full text-sm">
      <thead>
        <tr class="border-b border-border-subtle text-left text-xs uppercase text-text-subtle">
          <th class="py-2 pr-3 font-medium">Provider</th>
          <th class="py-2 pr-3 font-medium">Auth</th>
          <th class="py-2 pr-3 font-medium">Status</th>
          <th class="py-2 pr-3 font-medium">Models</th>
          <th class="py-2 pr-3 font-medium">Quota</th>
          <th class="py-2 pr-3 font-medium">Last verified</th>
          <th class="py-2 font-medium">Actions</th>
        </tr>
      </thead>
      <tbody>
        {#each rows as row (row.id)}
          <tr class="border-b border-border-subtle/50 align-top">
            <td class="py-2 pr-3">
              <div class="font-medium text-text-main">{row.id}</div>
              {#if row.notes}
                <div class="mt-0.5 max-w-md text-xs text-text-muted">{row.notes}</div>
              {/if}
            </td>
            <td class="py-2 pr-3 text-xs text-text-muted">
              {row.auth.join(', ')}
            </td>
            <td class="py-2 pr-3">
              <Badge tone={stateTone[row.status]} size="sm">{row.status}</Badge>
            </td>
            <td class="py-2 pr-3 tabular-nums text-text-main">
              {row.models_available > 0 ? row.models_available : '—'}
            </td>
            <td class="py-2 pr-3 text-xs text-text-muted">
              {row.quota}
              <div class="text-[11px] text-text-subtle">via {row.quota_source}</div>
            </td>
            <td class="py-2 pr-3 text-xs text-text-muted">
              {verifiedAtLabel(row.verified_at)}
              {#if row.verified_model}
                <div class="text-[11px] text-text-subtle">
                  {row.verified_model}{row.latency_ms ? ` · ${row.latency_ms}ms` : ''}
                </div>
              {/if}
            </td>
            <td class="py-2">
              <div class="flex flex-wrap gap-2">
                {#if canValidate(row)}
                  <Button
                    size="sm"
                    variant="secondary"
                    disabled={busy[row.id] !== undefined}
                    onclick={() => run(row.id, 'validate')}
                  >
                    {busy[row.id] === 'validate' ? 'Checking…' : 'Check key'}
                  </Button>
                {/if}
                {#if canSmoke(row)}
                  <Button
                    size="sm"
                    disabled={busy[row.id] !== undefined}
                    onclick={() => run(row.id, 'smoke')}
                  >
                    {busy[row.id] === 'smoke' ? 'Testing…' : 'Test'}
                  </Button>
                {/if}
              </div>
              {#if messages[row.id]}
                <p class="mt-1 max-w-xs text-[11px] text-text-muted">{messages[row.id]}</p>
              {/if}
            </td>
          </tr>
        {:else}
          <tr>
            <td colspan="7" class="py-6 text-center text-sm text-text-muted">
              {loading ? 'Loading providers…' : 'No providers registered.'}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</Card>
