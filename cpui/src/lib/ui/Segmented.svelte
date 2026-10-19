<script lang="ts" generics="T extends string">
	// Segmented control (DESIGN 4.4): filters and pickers.
	let {
		options,
		value = $bindable(),
		onchange,
		/** On control glass (default) or flat inside a pane. */
		glass = true,
		size = 'md'
	}: {
		options: { value: T; label: string; count?: number | string; title?: string }[];
		value: T;
		onchange?: (v: T) => void;
		glass?: boolean;
		size?: 'sm' | 'md';
	} = $props();
</script>

<div class="seg {size}" class:lg={glass} class:flat={!glass} role="tablist">
	{#each options as o (o.value)}
		<button
			type="button"
			role="tab"
			aria-selected={value === o.value}
			class:on={value === o.value}
			title={o.title}
			onclick={() => {
				value = o.value;
				onchange?.(o.value);
			}}
			>{o.label}{#if o.count !== undefined}<span class="n">{o.count}</span>{/if}</button
		>
	{/each}
</div>

<style>
	.flat {
		background: var(--p1);
		border: 1px solid var(--ln);
	}
	.sm > button {
		font-size: 11.5px;
		padding: 3px 9px;
		border-radius: 8px;
	}
</style>
