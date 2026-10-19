<script lang="ts">
	import { I } from './icons';
	// Inline error (DESIGN 4.17): short cause, mono detail folded, one action
	// (Retry by default), optionally dismissable.
	let {
		cause,
		detail,
		onretry,
		action = `${I.retry} Retry`,
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
		<span class="x">✕</span>
		<span class="grow">{cause}</span>
		{#if onretry}<button class="btn-sm" onclick={onretry}>{action}</button>{/if}
		{#if ondismiss}<button class="btn-icon" aria-label="Dismiss" onclick={ondismiss}>×</button>{/if}
	</div>
	{#if detail}
		<details>
			<summary class="disclosure tiny mut">Details</summary>
			<pre class="log">{detail}</pre>
		</details>
	{/if}
</div>

<style>
	.ec {
		border-radius: var(--r-btn);
		background: var(--rosBg);
		border: 1px solid var(--rosLn);
		color: var(--tx);
		padding: 8px 10px;
		display: flex;
		flex-direction: column;
		gap: 6px;
		font-size: var(--fs-ui);
	}
	.x {
		color: var(--ros);
		font-weight: 700;
	}
	summary {
		margin-bottom: 4px;
	}
</style>
