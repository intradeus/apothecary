<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	import { I } from './icons';
	// Inline error (DESIGN 4.17): short cause, mono detail folded, one action
	// (Retry by default), optionally dismissable.
	let {
		cause,
		detail,
		onretry,
		action,
		ondismiss
	}: {
		cause: string;
		detail?: string;
		onretry?: () => void;
		/** The action's label ("View log"); defaults to Retry. */
		action?: string;
		ondismiss?: () => void;
	} = $props();
</script>

<div class="ec materialize" role="alert">
	<div class="row nowrap">
		<span class="x">{I.bad}</span>
		<span class="grow">{cause}</span>
		{#if onretry}<button class="btn-sm" onclick={onretry}
				>{action ?? `${I.retry} ${t('ui.error.retry')}`}</button
			>{/if}
		{#if ondismiss}<button class="btn-icon" aria-label={t('ui.dismiss')} onclick={ondismiss}
				>{I.close}</button
			>{/if}
	</div>
	{#if detail}
		<details>
			<summary class="disclosure tiny mut">{t('ui.error.details')}</summary>
			<pre class="log">{detail}</pre>
		</details>
	{/if}
</div>

<style>
	.ec {
		border-radius: var(--r-btn);
		background: var(--rosSolid);
		border: 1px solid var(--rosLn);
		color: var(--tx);
		padding: 8px 10px;
		display: flex;
		flex-direction: column;
		gap: 6px;
		font-size: var(--fs-ui);
	}
	.x {
		color: var(--rosTx);
		font-weight: 700;
	}
	summary {
		margin-bottom: 4px;
	}
</style>
