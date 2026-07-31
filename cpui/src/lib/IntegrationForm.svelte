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

  // Descriptor help is plain text (it comes from the Go kind), but the URLs in
  // it are the whole point — "go here to make a token" is useless if you have
  // to copy it by hand. Split into text/link parts and render anchors.
  type Part = { text: string; href?: string };
  function linkify(s: string): Part[] {
    const parts: Part[] = [];
    const re = /https?:\/\/[^\s<>()]+[^\s<>().,;:]/g;
    let last = 0;
    for (const m of s.matchAll(re)) {
      const i = m.index ?? 0;
      if (i > last) parts.push({ text: s.slice(last, i) });
      parts.push({ text: m[0], href: m[0] });
      last = i + m[0].length;
    }
    if (last < s.length) parts.push({ text: s.slice(last) });
    return parts;
  }

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
  {#if descriptor.help}
    <details class="help" open={!existing}>
      <summary>How to set this up</summary>
      <div class="helptext">
        {#each linkify(descriptor.help) as p, i (i)}
          {#if p.href}
            <a href={p.href} target="_blank" rel="noreferrer">{p.text}</a>
          {:else}{p.text}{/if}
        {/each}
      </div>
    </details>
  {/if}
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
    {#if f.help}
      <div class="fieldhelp small muted">
        {#each linkify(f.help) as p, i (i)}
          {#if p.href}
            <a href={p.href} target="_blank" rel="noreferrer">{p.text}</a>
          {:else}{p.text}{/if}
        {/each}
      </div>
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

<style>
  .help {
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 8px 10px;
    margin-bottom: 12px;
  }
  .help summary {
    cursor: pointer;
    font-size: 13px;
  }
  .helptext {
    white-space: pre-wrap;
    font-size: 13px;
    line-height: 1.5;
    margin-top: 8px;
    color: var(--muted);
  }
  .fieldhelp {
    margin: -4px 0 6px;
  }
</style>
