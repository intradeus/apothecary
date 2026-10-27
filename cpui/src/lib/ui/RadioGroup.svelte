<script lang="ts" module>
	export interface ChoiceOption<V> {
		value: V;
		label: string;
		/** A quieter second part after the label. */
		detail?: string;
		disabled?: boolean;
	}

	let seq = 0;
</script>

<script lang="ts" generics="T">
	// A set of radios (DESIGN 4.14): pick exactly one. Stacked by default;
	// `row` lays short options side by side (yes / no). For a filter or a
	// view switch, use Segmented; for a long list, Select.
	//
	//   <RadioGroup bind:value={db} options={[{ value: 'pg', label: 'Postgres' }]} />
	import Radio from './Radio.svelte';

	let {
		options,
		value = $bindable(),
		name = `radio-${++seq}`,
		row = false,
		disabled = false,
		label,
		onchange
	}: {
		options: ChoiceOption<T>[];
		value: T | undefined;
		name?: string;
		row?: boolean;
		disabled?: boolean;
		/** Accessible name, when no legend names the set. */
		label?: string;
		onchange?: (v: T) => void;
	} = $props();
</script>

<div class="choices" class:row role="radiogroup" aria-label={label}>
	{#each options as o, i (i)}
		<Radio
			bind:group={value}
			value={o.value}
			{name}
			label={o.label}
			detail={o.detail}
			disabled={disabled || o.disabled}
			{onchange}
		/>
	{/each}
</div>
