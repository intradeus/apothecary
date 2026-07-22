<script lang="ts">
  // A file picker with removable chips + image thumbnails. The parent owns
  // the selected files (bindable) and uploads them on submit via
  // api.uploadArtifacts. Documents/images attached here reach the agent.
  let { files = $bindable([]), label = 'Attach files' }: { files?: File[]; label?: string } =
    $props();

  let inputEl = $state<HTMLInputElement | null>(null);
  const urls = new Map<File, string>();
  function preview(f: File): string {
    let u = urls.get(f);
    if (!u) {
      u = URL.createObjectURL(f);
      urls.set(f, u);
    }
    return u;
  }

  function onPick(e: Event) {
    const picked = (e.target as HTMLInputElement).files;
    if (picked) files = [...files, ...Array.from(picked)];
    if (inputEl) inputEl.value = ''; // allow re-picking the same file
  }
  const remove = (i: number) => (files = files.filter((_, j) => j !== i));
</script>

<div class="attach">
  <button type="button" class="small" onclick={() => inputEl?.click()}>📎 {label}</button>
  <input bind:this={inputEl} type="file" multiple hidden onchange={onPick} />
  {#if files.length}
    <div class="chips">
      {#each files as f, i (i)}
        <span class="chip">
          {#if f.type.startsWith('image/')}<img src={preview(f)} alt={f.name} />{/if}
          <span class="name" title={f.name}>{f.name}</span>
          <button type="button" class="x" onclick={() => remove(i)} aria-label="Remove">×</button>
        </span>
      {/each}
    </div>
  {/if}
</div>

<style>
  .attach {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 3px 6px;
    border: 1px solid var(--border, #ccc);
    border-radius: 6px;
    font-size: 12px;
    max-width: 220px;
  }
  .chip img {
    width: 22px;
    height: 22px;
    object-fit: cover;
    border-radius: 3px;
  }
  .chip .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .chip .x {
    border: none;
    background: none;
    cursor: pointer;
    font-size: 14px;
    line-height: 1;
    padding: 0 2px;
  }
</style>
