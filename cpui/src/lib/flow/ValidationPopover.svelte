<script lang="ts">
	// The Validate popover: the last validation or save result, its errors and
	// its advisory warnings.
	import type { Validation } from '$lib/flow/flowEditor.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import CheckChip from '$lib/ui/CheckChip.svelte';
	import Popover from '$lib/ui/Popover.svelte';

	let {
		anchor,
		validation,
		open = $bindable(false)
	}: {
		anchor: HTMLElement | null;
		validation: Validation | null;
		open: boolean;
	} = $props();
</script>

<Popover {anchor} bind:open placement="below-end" width={380} title={t('page.flow.validation')}>
	{#if validation}
		<div class="vp">
			<div class="vh">
				<CheckChip
					tone={validation.ok ? (validation.warnings?.length ? 'warn' : 'ok') : 'bad'}
					label={validation.title}
				/>
			</div>
			{#if validation.stages !== undefined || validation.hash}
				<div class="vm">
					{#if validation.stages !== undefined}<span
							>{t('page.flow.stagesN', { count: validation.stages })}</span
						>{/if}
					{#if validation.hash}<span class="mono">@{validation.hash.slice(0, 12)}</span>{/if}
				</div>
			{/if}
			{#if validation.errors?.length}
				<ul class="vl error">
					{#each validation.errors as e, i (i)}<li class="mono">{e}</li>{/each}
				</ul>
			{/if}
			{#if validation.warnings?.length}
				<div class="vw">
					{t('page.flow.bindingWarnings')} <span class="faint">— {t('page.flow.advisory')}</span>
				</div>
				<ul class="vl warn">
					{#each validation.warnings as w (w)}<li>{w}</li>{/each}
				</ul>
			{/if}
		</div>
	{:else}
		<div class="vp muted small">{t('page.flow.checking')}</div>
	{/if}
</Popover>

<style>
	.vp {
		padding: 2px 8px 8px;
		display: flex;
		flex-direction: column;
		gap: 8px;
		font-size: var(--fs-ui);
	}
	.vm {
		display: flex;
		gap: 12px;
		color: var(--text-muted);
		font-size: var(--fs-label);
	}
	.vl {
		margin: 0;
		padding-left: 16px;
		display: flex;
		flex-direction: column;
		gap: 4px;
		white-space: normal;
		max-height: 300px;
		overflow: auto;
	}
	.vl.error li {
		font-size: var(--fs-meta);
		color: var(--danger-text);
		white-space: pre-wrap;
	}
	.vl.warn li {
		font-size: var(--fs-label);
		color: var(--attention-text);
	}
	.vw {
		font-size: var(--fs-label);
		font-weight: 600;
	}
</style>
