<script lang="ts">
	// Number + unit (m / h / d / never) over an engine duration string
	// (DESIGN 7.10). Replaces free text: nothing to mistype.
	import { fromDur, toDur, type DurUnit } from './duration';

	let {
		value = $bindable(''),
		allowNever = false,
		disabled = false,
		id,
		label
	}: {
		value: string;
		allowNever?: boolean;
		disabled?: boolean;
		id?: string;
		/** Accessible name for the number input. */
		label?: string;
	} = $props();

	const parsed = $derived(toDur(value));
	// Remembered so flipping to "never" and back keeps the number.
	let lastN = $state(24);
	let draft = $state<string | null>(null);

	const unit = $derived<DurUnit>(parsed?.unit ?? 'h');
	const shown = $derived(draft ?? (parsed && parsed.unit !== 'never' ? String(parsed.n) : ''));

	function setN(raw: string) {
		draft = raw;
		const n = Number(raw);
		if (!raw || !Number.isFinite(n) || n <= 0) return;
		lastN = n;
		value = fromDur({ n, unit: unit === 'never' ? 'h' : unit });
	}

	function setUnit(u: DurUnit) {
		draft = null;
		if (u === 'never') {
			if (parsed && parsed.unit !== 'never') lastN = parsed.n;
			value = 'never';
			return;
		}
		const n = parsed && parsed.unit !== 'never' ? parsed.n : lastN;
		value = fromDur({ n, unit: u });
	}

	const invalid = $derived(
		!disabled &&
			(parsed === null ||
				(draft !== null && (!(Number(draft) > 0) || !Number.isFinite(Number(draft)))))
	);
</script>

<div class="dur" class:invalid>
	<input
		{id}
		type="number"
		min="1"
		step="any"
		inputmode="decimal"
		aria-label={label}
		value={shown}
		placeholder={unit === 'never' ? '—' : ''}
		disabled={disabled || unit === 'never'}
		oninput={(e) => setN(e.currentTarget.value)}
		onblur={() => (draft = null)}
	/>
	<select
		value={unit}
		{disabled}
		aria-label="unit"
		onchange={(e) => setUnit(e.currentTarget.value as DurUnit)}
	>
		<option value="m">minutes</option>
		<option value="h">hours</option>
		<option value="d">days</option>
		{#if allowNever || unit === 'never'}<option value="never">never</option>{/if}
	</select>
</div>
{#if parsed === null && value}
	<div class="err">
		The engine holds <code>{value}</code>, which this picker cannot read — pick a value to replace
		it.
	</div>
{/if}

<style>
	.dur {
		display: grid;
		grid-template-columns: minmax(70px, 110px) minmax(110px, 140px);
		gap: 6px;
	}
	.dur input {
		font-family: var(--mono);
		font-size: 12.5px;
		text-align: right;
	}
	.invalid input {
		border-color: var(--ros);
	}
</style>
