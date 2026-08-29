<script lang="ts">
  // Stored files as clickable chips: image thumbnails inline, a glyph
  // otherwise. kind picks the byte route — attachments (human-given) vs
  // snapshots (engine-captured record).
  import { api } from '$lib/api';
  import { fmtBytes } from '$lib/format';

  export interface FileChip {
    id: string;
    filename: string;
    mime?: string;
    size?: number;
    kind: 'attachment' | 'snapshot';
  }

  let { items, label = '' }: { items: FileChip[]; label?: string } = $props();

  const url = (f: FileChip) =>
    f.kind === 'snapshot' ? api.files.snapshotUrl(f.id) : api.files.attachmentUrl(f.id);
</script>

{#if items.length}
  <div class="files">
    {#if label}<div class="small muted">{label}</div>{/if}
    <div class="chips">
      {#each items as f (f.kind + f.id)}
        <a class="file" href={url(f)} target="_blank" rel="noreferrer"
           title={`${f.filename} — ${f.mime ?? ''} ${fmtBytes(f.size)}`}>
          {#if f.mime?.startsWith('image/')}
            <img src={url(f)} alt={f.filename} />
          {:else}
            <span class="ico">📄</span>
          {/if}
          <span class="name">{f.filename}</span>
          <span class="size muted">{fmtBytes(f.size)}</span>
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
