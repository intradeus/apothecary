<script lang="ts">
  // Generic integration form rendered from the kind's Descriptor fields
  // (DESIGN2 §4/§11): adding a backend kind needs no frontend change.
  // Secrets are write-only: on edit, an empty secret input means "keep".
  import { api, type Integration, type KindDescriptor } from '$lib/api';

  let {
    descriptor,
    existing = null,
    onsaved,
    oncancel
  }: {
    descriptor: KindDescriptor;
    existing?: Integration | null;
    onsaved: (saved: Integration) => void;
    oncancel?: () => void;
  } = $props();

  const fields = descriptor.fields ?? [];
  let name = $state(existing?.name ?? descriptor.label);
  let values = $state<Record<string, string>>(
    Object.fromEntries(
      fields
        .filter((f) => !f.secret)
        .map((f) => [f.name, String(existing?.config?.[f.name] ?? f.default ?? '')])
    )
  );
  let secretValues = $state<Record<string, string>>(
    Object.fromEntries(fields.filter((f) => f.secret).map((f) => [f.name, '']))
  );
  let busy = $state(false);
  let error = $state('');

  const isMultiline = (n: string) => n.endsWith('_json');

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      const config: Record<string, unknown> = {};
      for (const f of fields.filter((f) => !f.secret)) {
        const v = values[f.name]?.trim() ?? '';
        if (v !== '') config[f.name] = v;
      }
      // Only send secret keys the user actually typed — absent keys keep
      // their stored value (§3 merge semantics).
      const secrets: Record<string, string> = {};
      for (const [k, v] of Object.entries(secretValues)) {
        if (v.trim() !== '') secrets[k] = v;
      }
      const saved = existing
        ? await api.updateIntegration(existing.id, { name: name.trim(), config, secrets })
        : await api.createIntegration({
            port: descriptor.port,
            kind: descriptor.kind,
            name: name.trim(),
            config,
            secrets
          });
      onsaved(saved);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }
</script>

<form onsubmit={submit}>
  <label for="if-name">Name</label>
  <input id="if-name" bind:value={name} required />
  {#each fields as f (f.name)}
    <label for={`if-${f.name}`}>
      {f.label}{#if f.secret}
        <span class="muted">
          — {existing?.secrets?.[f.name]
            ? `set (${existing.secrets[f.name]}); leave empty to keep`
            : 'write-only'}</span
        >{/if}
    </label>
    {#if f.secret && isMultiline(f.name)}
      <textarea
        id={`if-${f.name}`}
        bind:value={secretValues[f.name]}
        placeholder={existing?.secrets?.[f.name] ? '(unchanged)' : (f.placeholder ?? '')}
        rows="4"
        class="mono"
      ></textarea>
    {:else if f.secret}
      <input
        id={`if-${f.name}`}
        type="password"
        bind:value={secretValues[f.name]}
        placeholder={existing?.secrets?.[f.name] ? '(unchanged)' : (f.placeholder ?? '')}
        required={f.required && !existing?.secrets?.[f.name]}
        autocomplete="off"
      />
    {:else}
      <input
        id={`if-${f.name}`}
        bind:value={values[f.name]}
        placeholder={f.placeholder ?? f.default ?? ''}
        required={f.required && !f.default}
      />
    {/if}
  {/each}
  {#if error}<div class="error">{error}</div>{/if}
  <div class="row" style="margin-top:14px">
    <button class="primary" type="submit" disabled={busy}>
      {busy ? 'Saving…' : existing ? 'Save changes' : `Add ${descriptor.label}`}
    </button>
    {#if oncancel}<button type="button" onclick={oncancel}>Cancel</button>{/if}
  </div>
</form>
