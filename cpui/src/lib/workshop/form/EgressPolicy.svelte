<script lang="ts">
	// Egress firewall. Allowlist (deny-all, permit these) vs denylist
	// (allow-all, block these). Both lists are kept so flipping the mode never
	// loses the other. blocked_domains is the live force-block managed from
	// the run page: shown here, carried through untouched.
	import { errorMessage } from '$lib/core/errors';
	import { api } from '$lib/api';
	import Segmented from '$lib/ui/Segmented.svelte';
	import TagInput from '$lib/ui/TagInput.svelte';
	import { I } from '$lib/design/icons';
	import { t, th } from '$lib/i18n/index.svelte';
	import FormSub from './FormSub.svelte';

	let {
		blockedDomains,
		egressMode = $bindable(),
		allowDomains = $bindable(),
		denyDomains = $bindable(),
		onerror
	}: {
		blockedDomains: string[];
		egressMode: 'allow' | 'deny';
		allowDomains: string[];
		denyDomains: string[];
		/** Fetching the defaults failed: said with the form's own error. */
		onerror: (message: string) => void;
	} = $props();

	let defaultsBusy = $state(false);
	let defaultsAdded = $state<number | null>(null);

	async function addDefaults() {
		defaultsBusy = true;
		defaultsAdded = null;
		try {
			const domains = await api.egress.defaults();
			const merged = [...allowDomains];
			for (const d of domains) if (!merged.includes(d)) merged.push(d);
			defaultsAdded = merged.length - allowDomains.length;
			allowDomains = merged;
		} catch (e) {
			onerror(errorMessage(e));
		} finally {
			defaultsBusy = false;
		}
	}
	const domainCheck = (v: string) =>
		/^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$/i.test(v)
			? null
			: t('form.project.notHost');
</script>

<FormSub>
	<h3>{t('form.project.egress')}</h3>
	<div class="row modes">
		<Segmented
			glass={false}
			label={t('form.project.egressMode')}
			options={[
				{ value: 'allow', label: t('form.project.allowlist') },
				{ value: 'deny', label: t('form.project.denylist') }
			]}
			bind:value={egressMode}
		/>
		<div class="mexp">
			<div class:cur={egressMode === 'allow'}>{@html th('form.project.allowExplain')}</div>
			<div class:cur={egressMode === 'deny'}>{@html th('form.project.denyExplain')}</div>
		</div>
	</div>

	<div class="row">
		<h3>
			{egressMode === 'deny' ? t('form.project.blockedHosts') : t('form.project.allowedHosts')}
		</h3>
		<span class="count">{egressMode === 'deny' ? denyDomains.length : allowDomains.length}</span>
		<span class="faint tiny">{t('form.project.otherKept')}</span>
		<span class="spacer"></span>
		{#if egressMode === 'allow'}
			{#if defaultsAdded !== null}<span class="faint tiny"
					>{defaultsAdded
						? t('form.project.defaultsAdded', { count: defaultsAdded })
						: t('form.project.defaultsPresent')}</span
				>{/if}
			<button
				type="button"
				class="btn-sm"
				onclick={addDefaults}
				disabled={defaultsBusy}
				aria-busy={defaultsBusy}
				>{defaultsBusy
					? t('form.project.loading')
					: `${I.add} ${t('form.project.addDefaults')}`}</button
			>
		{/if}
	</div>
	{#if egressMode === 'allow'}
		<TagInput
			bind:values={allowDomains}
			placeholder={t('form.project.allowPlaceholder')}
			validate={domainCheck}
		/>
	{:else}
		<TagInput
			bind:values={denyDomains}
			placeholder={t('form.project.denyPlaceholder')}
			validate={domainCheck}
		/>
	{/if}
	<div class="help">{t('form.project.hostsHelp')}</div>
	{#if blockedDomains.length}
		<div class="chips">
			<span class="faint tiny"
				>{t('form.project.forceBlocked', { count: blockedDomains.length })}</span
			>
			{#each blockedDomains as d (d)}<span class="pill sm mono tint-danger">{d}</span>{/each}
		</div>
	{/if}
</FormSub>

<style>
	.modes {
		align-items: flex-start;
		gap: 14px;
		margin-bottom: 6px;
	}
	.mexp {
		flex: 1;
		min-width: 240px;
		display: flex;
		flex-direction: column;
		gap: 3px;
		font-size: var(--fs-label);
		color: var(--text-faint);
		transition: color var(--dur-fade) var(--ease-out);
	}
	.mexp > div {
		transition: color var(--dur-fade) var(--ease-out);
	}
	.mexp .cur {
		color: var(--text);
	}
	.mexp :global(b) {
		font-weight: 600;
	}
</style>
