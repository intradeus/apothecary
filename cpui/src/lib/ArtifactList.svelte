<script lang="ts">
  // Stored attachments (a ticket's, a run's) as clickable chips: image
  // thumbnails render inline, everything else gets a file glyph. Each chip
  // links to the artifact route, which serves the bytes with their real
  // content type.
  import { api, fmtBytes, type ArtifactMeta } from '$lib/api';

  let { items, label = '' }: { items: ArtifactMeta[]; label?: string } = $props();
</script>

{#if items.length}
  <div class="files">
    {#if label}<div class="small muted">{label}</div>{/if}
    <div class="chips">
      {#each items as a (a.id)}
        <a class="file" href={api.artifactUrl(a.id)} target="_blank" rel="noreferrer"
           title={`${a.filename} — ${a.mime ?? ''} ${fmtBytes(a.size)}`}>
          {#if a.mime?.startsWith('image/')}
            <img src={api.artifactUrl(a.id)} alt={a.filename} />
          {:else}
            <span class="ico">📄</span>
          {/if}
          <span class="name">{a.filename}</span>
          <span class="size muted">{fmtBytes(a.size)}</span>
        </a>
      {/each}
    </div>
  </div>
{/if}

<style>
  .files {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .file {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 3px 8px;
    border: 1px solid var(--border);
    border-radius: 6px;
    font-size: 12px;
    max-width: 260px;
    text-decoration: none;
    color: inherit;
  }
  .file:hover {
    background: var(--hover, rgba(127, 127, 127, 0.12));
  }
  .file img {
    width: 22px;
    height: 22px;
    object-fit: cover;
    border-radius: 3px;
  }
  .file .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .file .size {
    font-size: 11px;
  }
</style>
