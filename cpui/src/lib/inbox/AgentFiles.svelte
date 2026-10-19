<script lang="ts">
	// The extra files an agent attached to a ping, beyond the reviewed
	// document: each a chip linking to its raise-time snapshot (images with a
	// thumbnail); one that was not captured reads as missing, never hidden.
	import { api } from '$lib/api';
	import type { PingFile } from '$lib/domain';
	import FileChip from '$lib/ui/FileChip.svelte';
	import { t } from '$lib/i18n/index.svelte';

	let { files, label }: { files: PingFile[]; label?: string } = $props();
</script>

{#if files.length}
	<div class="chips af">
		<span class="help">{label ?? t('inbox.fromAgent')}</span>
		{#each files as f (f.path)}
			{#if f.captured && f.snapshotId}
				<FileChip
					name={f.path}
					size={f.size}
					href={api.files.snapshotUrl(f.snapshotId)}
					thumb={f.mime?.startsWith('image/') ? api.files.snapshotUrl(f.snapshotId) : undefined}
				/>
			{:else}
				<FileChip name={f.path} state="missing" reason={t('inbox.notCaptured')} />
			{/if}
		{/each}
	</div>
{/if}

<style>
	.af {
		gap: 8px;
	}
</style>
