<script lang="ts">
	// A reference (review#5, build#3/decision#1) as a small mono chip: the
	// name a person quotes to an agent, or an agent to a person. A click
	// copies it and ticks; a refused clipboard says so in a toast. It sits
	// in rows that open something on click, so its own click stops there.
	// `plain` is the same chip without the button, for inside one.
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';
	import { toast } from './toast.svelte';

	let { ref, plain = false }: { ref: string; plain?: boolean } = $props();

	let copied = $state(false);
	let timer: ReturnType<typeof setTimeout> | undefined;

	async function copy(e: MouseEvent) {
		e.stopPropagation();
		e.preventDefault();
		clearTimeout(timer);
		try {
			await navigator.clipboard.writeText(ref);
			copied = true;
			timer = setTimeout(() => (copied = false), 1500);
		} catch {
			toast(t('ui.copy.failed'), { tone: 'bad' });
		}
	}
</script>

{#if plain}
	<span class="ref" title={t('ui.ref.label', { ref })}>{ref}</span>
{:else}
	<button
		type="button"
		class="ref"
		class:copied
		title={t('ui.ref.copy', { ref })}
		aria-label={t('ui.ref.copy', { ref })}
		onclick={copy}
		>{ref}{#if copied}<span class="tick" aria-hidden="true">{I.ok}</span>{/if}</button
	>
{/if}

<style>
	.ref {
		flex: none;
		display: inline-flex;
		align-items: center;
		gap: 4px;
		padding: 0 6px;
		border-radius: var(--r-tag);
		background: var(--fill-2);
		border: 1px solid var(--line);
		box-shadow: none;
		color: var(--text-muted);
		font-family: var(--font-code);
		font-size: var(--fs-id);
		font-weight: 500;
		line-height: 1.6;
		white-space: nowrap;
	}
	button.ref:hover {
		background: var(--fill-3);
		color: var(--text);
	}
	.copied {
		color: var(--success-text);
		border-color: var(--success-line);
	}
</style>
