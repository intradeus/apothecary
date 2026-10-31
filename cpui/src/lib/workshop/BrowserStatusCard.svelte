<script lang="ts">
	// The workshop's browser as it stands: whether its container runs, on
	// which image, and how many agent sessions use it. Read again after each
	// save (the toggle is a setup field), and on demand.
	import { t } from '$lib/i18n/index.svelte';
	import { api } from '$lib/api';
	import type { BrowserStatus } from '$lib/domain';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import { I } from '$lib/ui/icons';

	let {
		projectId,
		enabled,
		savedAt
	}: {
		projectId: string;
		/** The toggle as the form holds it (maybe not saved yet). */
		enabled: boolean;
		/** Changes on each save: the status is read again. */
		savedAt: number | null;
	} = $props();

	let info = $state<BrowserStatus | null>(null);
	let error = $state('');
	let loading = $state(false);

	async function load() {
		loading = true;
		error = '';
		try {
			info = await api.projects.browser(projectId);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		void savedAt;
		void load();
	});

	const tone = $derived(info?.state === 'running' ? 'grn' : 'mut');
	// The container runs an older image than this build would: recreated
	// once it is next idle.
	const outdated = $derived(!!info?.image && !!info.current && info.image !== info.current);
</script>

{#if error}
	<ErrorCard cause={t('form.browser.statusFail')} detail={error} onretry={load} />
{:else if !info}
	<Loading />
{:else}
	<div class="bs card-in">
		<div class="row hd">
			<span class="lbl">{t('form.browser.container')}</span>
			<span class="pill sm t-{tone}"
				><span class="dot"></span>{info.state === 'running'
					? t('form.browser.state.running')
					: info.state === 'stopped'
						? t('form.browser.state.stopped')
						: t('form.browser.state.absent')}</span
			>
			{#if info.state === 'running'}
				<span class="fnt small">{t('form.browser.sessions', { count: info.sessions })}</span>
			{/if}
			<span class="spacer"></span>
			<button
				type="button"
				class="btn-icon"
				title={t('form.browser.refresh')}
				aria-label={t('form.browser.refresh')}
				aria-busy={loading}
				onclick={load}>{I.retry}</button
			>
		</div>
		{#if !info.available}
			<div class="note">{t('form.browser.unavailable')}</div>
		{:else}
			{#if enabled !== info.enabled}
				<div class="fnt small">
					{enabled ? t('form.browser.pendingOn') : t('form.browser.pendingOff')}
				</div>
			{/if}
			<dl class="kv">
				{#if info.image || info.current}
					<dt>{t('form.browser.image')}</dt>
					<dd>
						<span class="mono">{info.image || info.current}</span>
						{#if outdated}
							<span
								class="pill sm t-gold"
								title={t('form.browser.outdatedTitle', { image: info.current })}
								>{t('form.browser.outdated')}</span
							>
						{/if}
					</dd>
				{/if}
				{#if info.container}
					<dt>{t('form.browser.name')}</dt>
					<dd class="mono">{info.container}</dd>
				{/if}
			</dl>
			<div class="help">{t('form.browser.lifecycle')}</div>
		{/if}
		{#if info.error}<div class="err mono">{info.error}</div>{/if}
	</div>
{/if}

<style>
	.bs {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 10px 12px;
		max-width: 620px;
		margin-top: 4px;
	}
	.hd {
		gap: 8px;
	}
	.bs .mono {
		font-size: var(--fs-meta);
		overflow-wrap: anywhere;
	}
	.err {
		white-space: pre-wrap;
	}
</style>
