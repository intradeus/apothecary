<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// One file, as a chip (DESIGN 4.15): a 28px thumbnail or glyph, the name,
	// a mono size or reason. Links out when it has an href, removable when it
	// has onremove; `missing` and `refused` read as such, never hidden.
	import { fmtBytes } from '$lib/format';
	import { I } from './icons';

	let {
		name,
		size,
		href,
		thumb,
		state = 'ok',
		reason,
		onremove
	}: {
		name: string;
		size?: number;
		href?: string;
		/** An image URL for the thumbnail. */
		thumb?: string;
		state?: 'ok' | 'missing' | 'refused';
		/** Why it's missing or refused, shown instead of the size. */
		reason?: string;
		onremove?: () => void;
	} = $props();
</script>

{#snippet body()}
	{#if thumb && state === 'ok'}<img src={thumb} alt="" />{:else}<span class="ico"
			>{state === 'ok' ? I.file : I.warn}</span
		>{/if}
	<span class="name ell" title={name}>{name}</span>
	<span class="size mono">{reason ?? (size !== undefined ? fmtBytes(size) : '')}</span>
{/snippet}

{#if href && state === 'ok'}
	<a class="fc" {href} target="_blank" rel="noreferrer" title={name}>{@render body()}</a>
{:else}
	<span class="fc {state}">
		{@render body()}
		{#if onremove}<button
				type="button"
				class="btn-icon btn-sm"
				aria-label={t('ui.tags.remove', { value: name })}
				onclick={onremove}>{I.close}</button
			>{/if}
	</span>
{/if}

<style>
	.fc {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		padding: 3px 10px 3px 3px;
		border-radius: var(--r-row);
		background: var(--p2);
		border: 1px solid var(--ln);
		font-size: var(--fs-label);
		max-width: 280px;
		min-width: 0;
		color: var(--tx);
	}
	a.fc:hover {
		background: var(--p3);
		text-decoration: none;
	}
	.fc:has(.btn-icon) {
		padding-right: 3px;
	}
	.refused {
		background: var(--rosBg);
		border-color: var(--rosLn);
		color: var(--rosTx);
	}
	.missing {
		border-style: dashed;
		background: transparent;
		color: var(--mut);
	}
	img,
	.ico {
		flex: none;
		width: 28px;
		height: 28px;
		border-radius: var(--r-icon);
		object-fit: cover;
	}
	.ico {
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--code);
		color: var(--mut);
		font-size: var(--fs-body);
	}
	.refused .ico {
		color: var(--rosTx);
		font-weight: 700;
	}
	.size {
		color: var(--mut);
		font-size: var(--fs-id);
		white-space: nowrap;
	}
	.refused .size {
		color: var(--rosTx);
	}
</style>
