<script lang="ts" generics="T">
	// A set of checkboxes (DESIGN 4.14): pick any number, none included.
	// `value` is the picked values, in the options' order.
	//
	//   <CheckboxGroup bind:value={picked} options={threads} />
	import Checkbox from './Checkbox.svelte';
	import type { ChoiceOption } from './RadioGroup.svelte';

	let {
		options,
		value = $bindable([]),
		row = false,
		disabled = false,
		label,
		onchange
	}: {
		options: ChoiceOption<T>[];
		value: T[];
		row?: boolean;
		disabled?: boolean;
		/** Accessible name, when no legend names the set. */
		label?: string;
		onchange?: (v: T[]) => void;
	} = $props();

	function set(v: T, on: boolean) {
		const next = options.map((o) => o.value).filter((x) => (x === v ? on : value.includes(x)));
		value = next;
		onchange?.(next);
	}
</script>

<div class="choices" class:row role="group" aria-label={label}>
	{#each options as o, i (i)}
		<Checkbox
			checked={value.includes(o.value)}
			label={o.label}
			detail={o.detail}
			disabled={disabled || o.disabled}
			onchange={(on) => set(o.value, on)}
		/>
	{/each}
</div>
