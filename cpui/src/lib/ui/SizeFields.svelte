<script lang="ts">
	// A sandbox size's two fields, CPUs and memory (GiB). An empty field is
	// unset (null): its placeholder says what applies instead.
	import type { SandboxSize } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';

	let {
		size = $bindable(),
		idPrefix,
		placeholder,
		invalid = [],
		disabled = false,
		onchange
	}: {
		size: SandboxSize;
		idPrefix: string;
		/** What an unset field means here (a default, "no limit"). */
		placeholder: { cpus: string; memoryGiB: string };
		/** Fields to mark invalid (above a ceiling, not positive). */
		invalid?: ('cpus' | 'memoryGiB')[];
		disabled?: boolean;
		onchange?: (size: SandboxSize) => void;
	} = $props();

	function set(key: 'cpus' | 'memoryGiB', raw: string) {
		const v = raw.trim() === '' ? null : Number(raw);
		size = { ...size, [key]: v };
		onchange?.(size);
	}
</script>

<div class="size">
	{#each [{ key: 'cpus', label: t('ui.size.cpus'), unit: t('ui.size.cpusUnit') }, { key: 'memoryGiB', label: t('ui.size.memory'), unit: t('ui.size.memoryUnit') }] as const as f (f.key)}
		<div class="f">
			<label class="muted small" for="{idPrefix}-{f.key}">{f.label}</label>
			<span class="in">
				<input
					id="{idPrefix}-{f.key}"
					class="mono"
					type="number"
					min="0"
					step="any"
					{disabled}
					aria-invalid={invalid.includes(f.key) || undefined}
					placeholder={placeholder[f.key]}
					value={size[f.key] ?? ''}
					oninput={(e) => set(f.key, e.currentTarget.value)}
				/>
				<span class="muted small">{f.unit}</span>
			</span>
		</div>
	{/each}
</div>

<style>
	.size {
		display: flex;
		flex-wrap: wrap;
		gap: 14px;
	}
	.f {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.in {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.in input {
		width: 7em;
	}
</style>
