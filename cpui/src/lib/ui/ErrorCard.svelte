<script lang="ts">
	// Inline error (DESIGN 4.17): short cause, mono detail folded, Retry.
	let { cause, detail, onretry }: { cause: string; detail?: string; onretry?: () => void } =
		$props();
</script>

<div class="ec" role="alert">
	<div class="row nowrap">
		<span class="x">✕</span>
		<span class="grow">{cause}</span>
		{#if onretry}<button class="btn-sm" onclick={onretry}>↻ Retry</button>{/if}
	</div>
	{#if detail}
		<details>
			<summary class="tiny mut">Details</summary>
			<pre class="log">{detail}</pre>
		</details>
	{/if}
</div>

<style>
	.ec {
		border-radius: 12px;
		background: var(--rosBg);
		border: 1px solid var(--rosLn);
		color: var(--tx);
		padding: 8px 10px;
		display: flex;
		flex-direction: column;
		gap: 6px;
		font-size: 12.5px;
	}
	.x {
		color: var(--ros);
		font-weight: 700;
	}
	summary {
		margin-bottom: 4px;
	}
</style>
