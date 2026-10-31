<script lang="ts" module>
	import { I } from '$lib/design/icons';
	import type { FileKind } from '$lib/review/reviewDoc';

	/** A shown file's glyph by what it is. */
	export const KIND_ICON: Record<FileKind, string> = {
		text: I.file,
		image: I.image,
		other: I.binary
	};

	export const baseName = (path: string) => path.split('/').pop() ?? path;
	export const dirName = (path: string) => path.split('/').slice(0, -1).join('/');
</script>

<script lang="ts">
	// The files a review shows, in the gate's order: each with what it is,
	// how many comments it carries and — once opened — a viewed mark. As a
	// list (the workbench's left column) or, where the frame is narrow, a
	// picker above the file (each variant shows only at its own width).
	import type { ShownFile } from './files.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';

	let {
		files,
		current,
		counts,
		viewed,
		variant = 'list',
		onpick
	}: {
		files: ShownFile[];
		current: string;
		/** Comments per path. */
		counts: Record<string, number>;
		/** Opened paths; absent where it means nothing (a submitted review). */
		viewed?: Set<string>;
		variant?: 'list' | 'select';
		onpick: (path: string) => void;
	} = $props();

	const fileOptions = $derived<SelectOption[]>(
		files.map((f) => {
			const n = counts[f.path] ?? 0;
			return {
				value: f.path,
				label: `${KIND_ICON[f.kind]} ${f.path}${n ? ` · ${t('doc.commentCount', { count: n })}` : ''}${
					viewed?.has(f.path) ? ` · ${t('doc.viewed')}` : ''
				}`
			};
		})
	);
</script>

{#if variant === 'list'}
	<nav class="fl" aria-label={t('doc.filesLabel')}>
		{#each files as f (f.path)}
			{@const n = counts[f.path] ?? 0}
			<button
				class="rowbtn fi"
				class:on={f.path === current}
				aria-current={f.path === current ? 'true' : undefined}
				title={f.path}
				onclick={() => onpick(f.path)}
			>
				<span class="ico" aria-hidden="true">{KIND_ICON[f.kind]}</span>
				<span class="truncate grow"
					>{baseName(f.path)}{#if dirName(f.path)}<span class="faint dir">
							{dirName(f.path)}</span
						>{/if}</span
				>
				{#if n}<span class="pill sm mono tint-brand">{n}</span>{/if}
				{#if viewed?.has(f.path) && f.path !== current}<span
						class="hue-success seen"
						title={t('doc.viewed')}
						aria-label={t('doc.viewed')}>{I.ok}</span
					>{/if}
			</button>
		{/each}
	</nav>
{:else if files.length > 1}
	<div class="pick">
		<span class="sel">
			<Select
				label={t('doc.filesLabel')}
				options={fileOptions}
				value={current}
				onchange={(v) => onpick(v)}
			/>
		</span>
		<span class="help"
			>{t('doc.fileOf', {
				n: files.findIndex((f) => f.path === current) + 1,
				count: files.length
			})}</span
		>
	</div>
{/if}

<style>
	.fl {
		padding: 6px;
		display: flex;
		flex-direction: column;
		gap: 1px;
		overflow: auto;
		flex: none;
		max-height: 45%;
	}
	.fi {
		display: flex;
		align-items: center;
		gap: 7px;
		padding: 5px 8px;
		font-size: var(--fs-label);
		color: var(--text-muted);
	}
	.fi:hover,
	.fi.on {
		color: var(--text);
	}
	.ico {
		width: 1.1em;
		text-align: center;
		flex: none;
	}
	.dir {
		margin-left: 6px;
		font-size: var(--fs-meta);
	}
	.seen {
		font-size: var(--fs-meta);
	}

	/* The picker shows only where the list is folded away. */
	.pick {
		display: none;
		align-items: center;
		gap: 10px;
		padding: 8px 12px;
		border-bottom: 1px solid var(--line);
	}
	.pick .sel {
		flex: 1;
		min-width: 0;
	}
	.pick .help {
		white-space: nowrap;
	}
	@container review (max-width: 1100px) {
		.pick {
			display: flex;
		}
	}
</style>
