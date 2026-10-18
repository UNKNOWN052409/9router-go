<script lang="ts">
  // Personas: per-provider / per-model custom system prompts.
  //
  // Scope is a provider id ("opencode" — every model on that lane) or a
  // provider/model pair ("opencode/mimo-v2.6-flash-free" — one model only).
  // Model-scoped entries win over provider-scoped ones for matching traffic.
  // The persona is injected into the system channel on the request path and
  // never reports itself as part of the model identity.
  import { onMount } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import Modal from '../lib/ui/Modal.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Input from '../lib/ui/Input.svelte'
  import Toggle from '../lib/ui/Toggle.svelte'
  import { api, type Settings } from '../api/client'
  import { PROVIDER_CATALOG } from '../lib/providers'

  interface Props {
    settings?: Settings
    onRefresh?: () => void
  }

  interface PersonaEntry {
    name: string
    description: string
    systemPrompt: string
    enabled: boolean
  }

  interface PersonaDraft {
    scope: string
    name: string
    description: string
    systemPrompt: string
    enabled: boolean
  }

  let { settings = {}, onRefresh }: Props = $props()

  let personas = $state<Record<string, PersonaEntry>>({})
  let saving = $state(false)
  let saveSuccess = $state(false)
  let saveError = $state('')
  let activePersona = $state<string | null>(null)
  let editing = $state<PersonaDraft | null>(null)
  let confirmResetScope = $state<string | null>(null)

  const PRESETS: { id: string; name: string; description: string; prompt: string }[] = [
    {
      id: 'concise-coding',
      name: 'Concise Coding',
      description: 'Direct, minimal, production-first outputs.',
      prompt:
        'You are an expert software engineer. Answer directly and write production-quality code. Prefer the smallest change that solves the problem. Never pad answers; skip pleasantries. When in doubt, state your assumption in one line before the answer.'
    },
    {
      id: 'creative-writing',
      name: 'Creative Writing',
      description: 'Vivid, expressive prose with a distinctive voice.',
      prompt:
        'You are a skilled storyteller. Write with vivid, concrete language and a distinctive voice. Show, don\'t tell. Vary sentence rhythm and keep the reader engaged. Match the register the user asks for, and enrich rather than merely answer.'
    },
    {
      id: 'technical-research',
      name: 'Technical Research',
      description: 'Structured, source-aware analysis with citations.',
      prompt:
        'You are a technical researcher. Structure answers with clear sections, separate fact from inference, and flag uncertainty. Prefer primary sources, name them explicitly, and give concrete steps over generalities.'
    }
  ]

  function scopeLabel(key: string): string {    const [provider, model] = key.split('/')
    const cat = PROVIDER_CATALOG.find((p) => p.id === provider)
    if (model) {
      return `${cat?.name ?? provider} · ${model}`
    }
    return cat?.name ?? provider
  }

  function syncFromSettings() {
    const raw = (settings as Record<string, unknown>).personas
    if (raw && typeof raw === 'object' && !Array.isArray(raw)) {
      const next: Record<string, PersonaEntry> = {}
      for (const [k, v] of Object.entries(raw as Record<string, unknown>)) {
        if (typeof v === 'string') {
          next[k] = { name: '', description: '', systemPrompt: v, enabled: true }
        } else if (v && typeof v === 'object') {
          const p = v as Record<string, unknown>
          next[k] = {
            name: typeof p.name === 'string' ? p.name : '',
            description: typeof p.description === 'string' ? p.description : '',
            systemPrompt: typeof p.systemPrompt === 'string' ? p.systemPrompt : '',
            enabled: p.enabled === true
          }
        }
      }
      personas = next
    } else {
      personas = {}
    }
  }

  $effect(() => {
    syncFromSettings()
  })

  onMount(() => {
    syncFromSettings()
  })

  const sortedScopes = $derived(Object.keys(personas).sort())

  async function savePersonas() {
    saving = true
    saveError = ''
    saveSuccess = false
    try {
      await api.updateSettings({ personas: personas as unknown as Record<string, unknown> })
      saveSuccess = true
      onRefresh?.()
      setTimeout(() => (saveSuccess = false), 3000)
    } catch (err) {
      saveError = err instanceof Error ? err.message : String(err)
    } finally {
      saving = false
    }
  }

  function openEditor(scope: string | null) {
    if (scope === null) {
      editing = {
        scope: '',
        name: '',
        description: '',
        systemPrompt: '',
        enabled: true
      }
    } else {
      const p = personas[scope]
      editing = {
        scope,
        name: p?.name ?? '',
        description: p?.description ?? '',
        systemPrompt: p?.systemPrompt ?? '',
        enabled: p?.enabled ?? true
      }
    }
    activePersona = scope
  }

  function applyPreset(id: string) {
    const preset = PRESETS.find((p) => p.id === id)
    if (preset && editing) {
      editing.name = editing.name || preset.name
      editing.description = editing.description || preset.description
      editing.systemPrompt = preset.prompt
    }
  }

  function saveEditor() {
    if (!editing) return
    const scope = editing.scope.trim()
    if (!scope) return
    const { systemPrompt } = editing
    if (systemPrompt.trim()) {
      personas[scope] = {
        name: editing.name.trim(),
        description: editing.description.trim(),
        systemPrompt,
        enabled: editing.enabled
      }
    } else {
      delete personas[scope]
    }
    editing = null
    activePersona = null
    void savePersonas()
  }

  function resetScope(scope: string) {
    delete personas[scope]
    confirmResetScope = null
    void savePersonas()
  }

  function toggleEnabled(scope: string, value: boolean) {
    if (personas[scope]) {
      personas[scope] = { ...personas[scope], enabled: value }
      void savePersonas()
    }
  }
</script>

<div class="space-y-4">
  <Card>
    <div class="p-4 flex flex-col gap-3">
      <div class="flex items-center justify-between gap-3">
        <div>
          <h2 class="text-lg font-bold text-text-main">Personas (Custom System Prompts)</h2>
          <p class="text-sm text-text-muted">
            Give each provider (or one model) its own identity. The prompt is injected into the
            system channel on every matching request, alongside Caveman / Ponytail / ADHD. A
            model-scoped entry overrides the provider-scoped one.
          </p>
        </div>
        <Button icon="psychology" onclick={() => openEditor(null)} disabled={saving}>
          Add Persona
        </Button>
      </div>

      {#if saveError}
        <p class="text-sm text-danger">{saveError}</p>
      {/if}
      {#if saveSuccess}
        <p class="text-sm text-green-600">Saved. Personas are applied to new requests immediately.</p>
      {/if}

      {#key sortedScopes}
        <div class="flex flex-col gap-2">
          {#each sortedScopes as scope (scope)}
            <div class="flex items-center gap-3 rounded-[10px] border border-border bg-surface-2 px-4 py-3">
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2">
                  <span class="font-medium text-text-main truncate">{scopeLabel(scope)}</span>
                  {#if personas[scope].name}
                    <span class="text-xs text-primary">{personas[scope].name}</span>
                  {/if}
                </div>
                <p class="text-xs text-text-muted truncate">
                  {personas[scope].description || personas[scope].systemPrompt.split('\n')[0]}
                </p>
              </div>
              <Toggle checked={personas[scope].enabled} onchange={(value) => toggleEnabled(scope, value)} />
              <Button size="sm" variant="outline" onclick={() => openEditor(scope)}>Edit</Button>
              <Button size="sm" variant="ghost" onclick={() => (confirmResetScope = scope)}>Remove</Button>
            </div>
          {/each}
        </div>
      {/key}

      {#if sortedScopes.length === 0}
        <p class="text-sm text-text-subtle">
          No personas yet. Add one to give a provider or model its own identity, or pick a preset.
        </p>
      {/if}
    </div>
  </Card>

  <Card>
    <div class="p-4">
      <h3 class="text-base font-bold text-text-main mb-2">Quick presets</h3>
      <div class="grid grid-cols-1 md:grid-cols-3 gap-2">
        {#each PRESETS as preset (preset.id)}
          <button
            type="button"
            class="rounded-[10px] border border-border bg-surface-2 p-3 text-left hover:border-primary/40 transition-colors cursor-pointer"
            onclick={() => {
              openEditor(null)
              if (editing) {
                editing.scope = ''
                applyPreset(preset.id)
              }
            }}
          >
            <div class="font-medium text-text-main text-sm">{preset.name}</div>
            <div class="text-xs text-text-muted">{preset.description}</div>
          </button>
        {/each}
      </div>
    </div>
  </Card>
</div>

{#if editing}
  <Modal
    isOpen={true}
    onClose={() => (editing = null)}
    title={`${activePersona ? 'Edit' : 'Add'} Persona`}
    size="xl"
  >
    <div class="space-y-4">
      <Input
        label="Scope"
        placeholder="provider or provider/model (e.g. opencode or opencode/mimo-v2.6-flash-free)"
        bind:value={editing.scope}
        hint="Provider id alone applies to every model on that lane."
        disabled={activePersona !== null}
      />
      <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
        <Input label="Persona name (optional)" placeholder="e.g. Go Reviewer" bind:value={editing.name} />
        <Input
          label="Description (optional)"
          placeholder="What this persona is for"
          bind:value={editing.description}
        />
      </div>
      <div>
        <label for="persona-system-prompt" class="block text-sm font-medium text-text-main mb-1"
          >System prompt</label
        >
        <textarea
          id="persona-system-prompt"
          class="w-full h-48 rounded-[10px] border border-border bg-surface-2 px-3 py-2 text-sm text-text-main font-mono resize-y focus:outline-none focus:border-primary"
          placeholder="You are…"
          bind:value={editing.systemPrompt}
        ></textarea>
      </div>
      <div class="flex items-center gap-2">
        <Toggle checked={editing.enabled} onchange={(value) => (editing!.enabled = value)} />
        <span class="text-sm text-text-muted">Enabled</span>
      </div>
      <div class="flex flex-wrap gap-2">
        {#each PRESETS as preset (preset.id)}
          <Button size="sm" variant="outline" onclick={() => applyPreset(preset.id)}>
            {preset.name}
          </Button>
        {/each}
      </div>
      <div class="flex justify-end gap-2 pt-2">
        <Button variant="ghost" onclick={() => (editing = null)}>Cancel</Button>
        <Button variant="primary" onclick={saveEditor} disabled={!editing.scope.trim()}>
          Save Persona
        </Button>
      </div>
    </div>
  </Modal>
{/if}

{#if confirmResetScope}
  <Modal isOpen={true} onClose={() => (confirmResetScope = null)} title="Remove persona" size="sm">
    <div class="space-y-4 p-1">
      <p class="text-sm text-text-muted">
        Remove the persona for <span class="font-medium text-text-main">{scopeLabel(confirmResetScope)}</span>?
        This does not change the model — it only stops injecting the custom system prompt.
      </p>
      <div class="flex justify-end gap-2">
        <Button variant="ghost" onclick={() => (confirmResetScope = null)}>Cancel</Button>
        <Button variant="danger" onclick={() => resetScope(confirmResetScope!)}>Remove</Button>
      </div>
    </div>
  </Modal>
{/if}
