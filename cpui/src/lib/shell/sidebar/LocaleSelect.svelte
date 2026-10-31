<script lang="ts">
	// The language, once there is more than one to pick from: by name, or by
	// code on the rail.
	import { i18n, LOCALES, t, type Locale } from '$lib/i18n/index.svelte';
	import Select from '$lib/ui/Select.svelte';

	let { rail = false }: { rail?: boolean } = $props();

	const localeIds = Object.keys(LOCALES) as Locale[];
	const multiLocale = localeIds.length > 1;
	const pickLocale = (v: Locale) => void i18n.set(v);
	const options = $derived(
		localeIds.map((id) => ({ value: id, label: rail ? id.toUpperCase() : LOCALES[id].label }))
	);
</script>

{#if multiLocale}
	<span class="lang" class:rlang={rail}>
		<Select
			size="sm"
			inline
			label={t('shell.sidebar.language')}
			title={t('shell.sidebar.language')}
			{options}
			value={i18n.locale}
			onchange={pickLocale}
		/>
	</span>
{/if}

<style>
	.lang {
		display: inline-flex;
		max-width: 96px;
	}
	/* The rail is narrow: room for the code and the chevron. */
	.rlang {
		max-width: 52px;
	}
</style>
