<script lang="ts">
	// One model provider credential (docs/model-providers-plan.md): its
	// provider and kind, the account it belongs to, and — for a login — how
	// long the engine-kept token is good for and when it was last refreshed.
	// Never a key or a token: the API sends none.
	import { errorMessage } from '$lib/core/errors';
	import type { Credential, ProviderInfo } from '$lib/domain';
	import { api } from '$lib/api';
	import { ago, fmtAbs } from '$lib/core/format';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import ConfigCard from '$lib/ui/ConfigCard.svelte';
	import CheckChip from '$lib/ui/CheckChip.svelte';
	import { t } from '$lib/i18n/index.svelte';

	let {
		credential: c,
		provider,
		usedBy = [],
		onchanged
	}: {
		credential: Credential;
		provider?: ProviderInfo;
		/** Harness integrations that reference it, by name. */
		usedBy?: string[];
		onchanged: () => void;
	} = $props();

	let testing = $state(false);
	let result = $state<{ ok: boolean; error?: string; warnings: string[]; at: number } | null>(null);
	let error = $state('');
	let removing = $state(false);
	let renaming = $state(false);
	let name = $state('');
	let now = $state(Date.now());

	$effect(() => {
		const timer = setInterval(() => (now = Date.now()), 30_000);
		return () => clearInterval(timer);
	});

	const summary = $derived([
		{ k: t('providers.provider'), v: provider?.name ?? c.provider },
		...(c.account ? [{ k: t('providers.account'), v: c.account }] : []),
		...(c.kind === 'oauth'
			? [
					{
						k: t('providers.validUntil'),
						v: c.expiresAt ? fmtAbs(c.expiresAt) : t('providers.never')
					},
					...(c.lastRefresh
						? [
								{
									k: t('providers.refreshed'),
									v: ago(c.lastRefresh, now),
									title: fmtAbs(c.lastRefresh)
								}
							]
						: [])
				]
			: [])
	]);

	async function test() {
		testing = true;
		try {
			const r = await api.credentials.test(c.id);
			result = { ...r, at: Date.now() };
		} catch (e) {
			result = {
				ok: false,
				error: errorMessage(e),
				warnings: [],
				at: Date.now()
			};
		} finally {
			testing = false;
		}
		onchanged(); // a test of a due login refreshes it: show the new expiry
	}

	async function rename(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		try {
			await api.credentials.rename(c.id, name.trim());
			renaming = false;
			onchanged();
		} catch (err) {
			error = errorMessage(err);
		}
	}

	async function remove() {
		if (usedBy.length) {
			error = t('providers.usedBy', { harnesses: usedBy.join(', ') });
			return;
		}
		const ok = await confirmDialog({
			title: t('providers.deleteTitle', { name: c.name }),
			body: t('providers.deleteBody'),
			action: t('providers.deleteAction')
		});
		if (!ok) return;
		error = '';
		removing = true;
		try {
			await api.credentials.remove(c.id);
			toast(t('providers.deleted', { name: c.name }));
			onchanged();
		} catch (e) {
			error = errorMessage(e);
		} finally {
			removing = false;
		}
	}
</script>

<ConfigCard
	title={c.name}
	kind={c.kind === 'oauth' ? t('providers.kindLogin') : t('providers.kindKey')}
	{summary}
	{error}
	open={renaming}
	sub={usedBy.length
		? t('providers.usedBy', { harnesses: usedBy.join(', ') })
		: t('providers.unused')}
>
	{#snippet status()}
		{#if c.status === 'disabled'}
			<CheckChip tone="bad" label={t('providers.disabled')} title={c.error ?? ''} />
		{:else if testing}
			<CheckChip tone="live" label={t('providers.testing')} />
		{:else if result}
			<CheckChip
				tone={!result.ok ? 'bad' : result.warnings.length ? 'warn' : 'ok'}
				label={!result.ok
					? (result.error ?? '')
					: result.warnings.length
						? t('providers.okUnverified')
						: t('providers.ok')}
				when={ago(Math.min(result.at, now), now)}
				title={[result.error ?? 'ok', ...result.warnings].join('\n')}
			/>
		{:else}
			<CheckChip tone="none" label={t('providers.notTested')} />
		{/if}
	{/snippet}
	{#snippet actions()}
		<button class="btn-sm" onclick={test} disabled={testing} aria-busy={testing}
			>{t('providers.test')}</button
		>
		<button
			class="btn-sm"
			class:btn-sel={renaming}
			aria-pressed={renaming}
			onclick={() => {
				name = c.name;
				renaming = !renaming;
			}}>{t('providers.rename')}</button
		>
		<span class="spacer"></span>
		<button class="btn-danger btn-sm" onclick={remove} aria-busy={removing}
			>{t('providers.delete')}</button
		>
	{/snippet}
	<form class="row" onsubmit={rename}>
		<input bind:value={name} required aria-label={t('providers.rename')} />
		<button class="btn-primary btn-sm" type="submit">{t('providers.renameSave')}</button>
	</form>
</ConfigCard>
