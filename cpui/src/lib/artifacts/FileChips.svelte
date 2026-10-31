<script lang="ts">
	// Stored files as read-only chips (DESIGN 4.15), each linking to the stored
	// file; images get a thumbnail. kind picks the byte route — attachments
	// (human-given) vs snapshots (engine-captured record).
	import { api } from '$lib/api';
	import FileChip from '$lib/ui/FileChip.svelte';

	export interface StoredFile {
		id: string;
		filename: string;
		mime?: string;
		size?: number;
		kind: 'attachment' | 'snapshot';
	}

	let { items, label = '' }: { items: StoredFile[]; label?: string } = $props();

	const url = (f: StoredFile) =>
		f.kind === 'snapshot' ? api.files.snapshotUrl(f.id) : api.files.attachmentUrl(f.id);
</script>

{#if items.length}
	<div class="files">
		{#if label}<div class="help">{label}</div>{/if}
		<div class="chips">
			{#each items as f (f.kind + f.id)}
				<FileChip
					name={f.filename}
					size={f.size}
					href={url(f)}
					thumb={f.mime?.startsWith('image/') ? url(f) : undefined}
				/>
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
		gap: 6px;
	}
</style>
