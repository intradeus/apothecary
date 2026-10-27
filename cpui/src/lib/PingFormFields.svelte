<script lang="ts">
	// A question's form, field by field (ACP's elicitation form): a string
	// picks one option (radios) or is typed, an array picks any (checkboxes),
	// a number or integer is a number, a boolean yes or no. The values are
	// the caller's (bound): PingCard and the review workbench send them.
	import type { FormValue, PingField, PingForm } from '$lib/domain';
	import { fieldLabel } from '$lib/ping';
	import CheckboxGroup from '$lib/ui/CheckboxGroup.svelte';
	import RadioGroup from '$lib/ui/RadioGroup.svelte';
	import { t } from '$lib/i18n/index.svelte';

	let {
		form,
		values = $bindable(),
		disabled = false
	}: {
		form: PingForm;
		values: Record<string, FormValue | undefined>;
		disabled?: boolean;
	} = $props();

	// One field alone needs no label of its own when the question asks it.
	const bare = $derived(form.fields.length === 1 && !form.fields[0].title);

	const inputType = (f: PingField) =>
		f.format === 'email'
			? 'email'
			: f.format === 'uri'
				? 'url'
				: f.format === 'date'
					? 'date'
					: f.format === 'date-time'
						? 'datetime-local'
						: 'text';

	const choices = (f: PingField) =>
		f.options.map((o) => ({ value: o.id, label: o.label, detail: o.detail }));
	const yesNo = [
		{ value: true, label: t('ping.yes') },
		{ value: false, label: t('ping.no') }
	];

	function setNumber(f: PingField, raw: string) {
		values[f.id] = raw === '' ? undefined : Number(raw);
	}

	// A date-time input speaks local "YYYY-MM-DDTHH:MM"; the field wants
	// RFC 3339.
	function setDateTime(f: PingField, raw: string) {
		values[f.id] = raw ? new Date(raw).toISOString() : undefined;
	}
</script>

<div class="fields">
	{#each form.fields as f (f.id)}
		<fieldset class="field" {disabled}>
			{#if !bare}
				<legend
					>{fieldLabel(f)}{#if f.required}<span class="req" title={t('ping.required')}>*</span
						>{/if}</legend
				>
			{/if}
			{#if f.type === 'array'}
				<CheckboxGroup
					options={choices(f)}
					label={fieldLabel(f)}
					bind:value={() => (values[f.id] as string[] | undefined) ?? [], (v) => (values[f.id] = v)}
				/>
			{:else if f.type === 'string' && f.options.length}
				<RadioGroup
					options={choices(f)}
					name={f.id}
					label={fieldLabel(f)}
					bind:value={() => values[f.id] as string | undefined, (v) => (values[f.id] = v)}
				/>
			{:else if f.type === 'boolean'}
				<RadioGroup
					options={yesNo}
					name={f.id}
					row
					label={fieldLabel(f)}
					bind:value={() => values[f.id] as boolean | undefined, (v) => (values[f.id] = v)}
				/>
			{:else if f.type === 'number' || f.type === 'integer'}
				<input
					aria-label={fieldLabel(f)}
					type="number"
					step={f.type === 'integer' ? 1 : 'any'}
					min={f.minimum}
					max={f.maximum}
					value={values[f.id] ?? ''}
					oninput={(e) => setNumber(f, e.currentTarget.value)}
				/>
			{:else if f.format === 'date-time'}
				<input
					aria-label={fieldLabel(f)}
					type="datetime-local"
					oninput={(e) => setDateTime(f, e.currentTarget.value)}
				/>
			{:else if f.format || f.pattern || (f.maxLength !== undefined && f.maxLength <= 200)}
				<input
					aria-label={fieldLabel(f)}
					type={inputType(f)}
					pattern={f.pattern}
					minlength={f.minLength}
					maxlength={f.maxLength}
					bind:value={values[f.id]}
				/>
			{:else}
				<textarea
					aria-label={fieldLabel(f)}
					rows="2"
					minlength={f.minLength}
					maxlength={f.maxLength}
					placeholder={bare ? t('ping.answerPlaceholder') : ''}
					bind:value={values[f.id]}></textarea>
			{/if}
			{#if f.description}<div class="help">{f.description}</div>{/if}
		</fieldset>
	{/each}
</div>

<style>
	.fields {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
</style>
