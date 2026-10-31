<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';
	// Domain / tag list input (DESIGN 4.14): chips with ×; paste splits on
	// whitespace and commas; Enter or comma commits; Backspace on empty pops.
	let {
		values = $bindable([]),
		placeholder,
		mono = true,
		validate
	}: {
		values: string[];
		placeholder?: string;
		mono?: boolean;
		/** Return an error message to refuse a value. */
		validate?: (v: string) => string | null;
	} = $props();

	let draft = $state('');
	let err = $state('');

	function add(raw: string) {
		err = '';
		const next = [...values];
		for (const v of raw
			.split(/[\s,]+/)
			.map((s) => s.trim())
			.filter(Boolean)) {
			const bad = validate?.(v);
			if (bad) {
				err = `${v}: ${bad}`;
				continue;
			}
			if (!next.includes(v)) next.push(v);
		}
		values = next;
	}

	function key(e: KeyboardEvent) {
		if (e.key === 'Enter' || e.key === ',') {
			e.preventDefault();
			if (draft.trim()) add(draft);
			draft = '';
		} else if (e.key === 'Backspace' && !draft && values.length) {
			values = values.slice(0, -1);
		}
	}

	function paste(e: ClipboardEvent) {
		const t = e.clipboardData?.getData('text') ?? '';
		if (/[\s,]/.test(t.trim())) {
			e.preventDefault();
			add(t);
		}
	}
</script>

<div class="fieldbox ti">
	{#each values as v (v)}
		<span class="pill sm tint-muted" class:mono
			>{v}<button
				type="button"
				class="btn-icon btn-sm"
				aria-label={t('ui.tags.remove', { value: v })}
				onclick={() => (values = values.filter((x) => x !== v))}>{I.close}</button
			></span
		>
	{/each}
	<input
		class="bare"
		bind:value={draft}
		placeholder={placeholder ?? t('ui.tags.add')}
		onkeydown={key}
		onpaste={paste}
		onblur={() => {
			if (draft.trim()) add(draft);
			draft = '';
		}}
	/>
</div>
{#if err}<div class="error">{err}</div>{/if}

<style>
	.ti {
		flex-wrap: wrap;
		gap: 5px;
		padding: 5px 6px;
		min-height: 36px;
	}
	.ti input {
		flex: 1;
		min-width: 120px;
		padding: 3px 4px;
	}
</style>
