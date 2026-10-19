<script lang="ts">
	// Stored files as read-only chips (DESIGN 4.15): image thumbnails inline,
	// a glyph otherwise; each links to the stored file. kind picks the byte
	// route — attachments (human-given) vs snapshots (engine-captured record).
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
		{#if label}<div class="lbl">{label}</div>{/if}
		<div class="chips">
			{#each items as f (f.kind + f.id)}
				<a
					class="fc"
					href={url(f)}
					target="_blank"
					rel="noreferrer"
					title={`${f.filename} — ${f.mime ?? ''} ${fmtBytes(f.size)}`}
				>
					{#if f.mime?.startsWith('image/')}
						<img src={url(f)} alt="" />
					{:else}
						<span class="ico">▤</span>
					{/if}
					<span class="name">{f.filename}</span>
					<span class="size mono">{fmtBytes(f.size)}</span>
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
	.lbl {
		font-size: 11.5px;
		color: var(--mut);
	}
	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}
	.fc {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		padding: 3px 10px 3px 3px;
		border-radius: 10px;
		background: var(--p2);
		border: 1px solid var(--ln);
		font-size: 12px;
		max-width: 280px;
		color: var(--tx);
	}
	.fc:hover {
		background: var(--p3);
		text-decoration: none;
	}
	.fc img,
	.ico {
		flex: none;
		width: 28px;
		height: 28px;
		border-radius: 7px;
		object-fit: cover;
	}
	.ico {
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--code);
		color: var(--mut);
		font-size: 13px;
	}
	.name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}
	.size {
		color: var(--mut);
		font-size: 10.5px;
		white-space: nowrap;
	}
</style>
