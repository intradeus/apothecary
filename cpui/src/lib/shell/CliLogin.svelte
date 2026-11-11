<script lang="ts">
	// Approving a terminal sign-in (`apothecary login`): the terminal printed
	// a code and this page's link; the person checks the code matches and
	// approves — the terminal then collects a token of theirs on its next
	// poll — or denies. The page sits behind sign-in like any other, rendered
	// in place, so the link's ?code= survives signing in first.
	import { api, isNotFound } from '$lib/api';
	import { errorMessage } from '$lib/core/errors';
	import type { CliLogin } from '$lib/domain';
	import { I } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';

	let { code }: { code: string } = $props();

	type Phase = 'loading' | 'ready' | 'gone' | 'approved' | 'denied';
	let phase = $state<Phase>('loading');
	let login = $state<CliLogin | null>(null);
	let loadError = $state('');
	let actError = $state('');
	let busy = $state<'approve' | 'deny' | null>(null);

	async function load(c: string) {
		phase = 'loading';
		loadError = '';
		if (!c.trim()) {
			phase = 'gone';
			return;
		}
		try {
			login = await api.auth.cliLogin(c.trim());
			phase = 'ready';
		} catch (e) {
			if (isNotFound(e)) phase = 'gone';
			else loadError = errorMessage(e);
		}
	}

	$effect(() => {
		void load(code);
	});

	async function answer(verb: 'approve' | 'deny') {
		if (!login) return;
		busy = verb;
		actError = '';
		try {
			if (verb === 'approve') await api.auth.approveCliLogin(login.userCode);
			else await api.auth.denyCliLogin(login.userCode);
			phase = verb === 'approve' ? 'approved' : 'denied';
		} catch (e) {
			if (isNotFound(e)) phase = 'gone';
			else actError = errorMessage(e);
		} finally {
			busy = null;
		}
	}
</script>

<PageHeader title={t('auth.cli.docTitle')} />

<div class="page glass">
	<div class="cli">
		{#if loadError}
			<ErrorCard cause={t('auth.cli.failed')} detail={loadError} onretry={() => load(code)} />
		{:else if phase === 'loading'}
			<Loading text={t('auth.cli.loading')} />
		{:else if phase === 'gone'}
			<EmptyState title={t('auth.cli.gone')} text={t('auth.cli.goneText')} />
		{:else if phase === 'approved'}
			<Callout hue="success" glyph={I.ok}>{t('auth.cli.approved')}</Callout>
		{:else if phase === 'denied'}
			<Callout hue="muted" glyph={I.close}>{t('auth.cli.denied')}</Callout>
		{:else if login}
			<section class="card-in ask" aria-labelledby="cli-title">
				<h2 id="cli-title">{t('auth.cli.title')}</h2>
				<div class="name">{login.name}</div>
				<div class="code mono" aria-label={t('auth.cli.codeLabel')}>{login.userCode}</div>
				<div class="muted">{t('auth.cli.check')}</div>
				<div class="help">{t('auth.cli.warning')}</div>
				{#if actError}
					<ErrorCard cause={t('auth.cli.failed')} detail={actError} />
				{/if}
				<div class="acts">
					<button
						class="btn-primary btn-md"
						disabled={busy !== null}
						aria-busy={busy === 'approve'}
						onclick={() => answer('approve')}>{t('auth.cli.approve')}</button
					>
					<button
						class="btn-md"
						disabled={busy !== null}
						aria-busy={busy === 'deny'}
						onclick={() => answer('deny')}>{t('auth.cli.deny')}</button
					>
				</div>
			</section>
		{/if}
	</div>
</div>

<style>
	.cli {
		max-width: 520px;
		margin: 24px auto;
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.ask {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 10px;
		padding: 24px 20px;
		text-align: center;
	}
	h2 {
		margin: 0;
		font-family: var(--font-display);
		font-size: var(--fs-section);
	}
	.name {
		font-weight: 600;
	}
	.code {
		font-size: var(--fs-empty);
		letter-spacing: 0.12em;
		padding: 6px 14px;
	}
	.acts {
		display: flex;
		gap: 8px;
		margin-top: 4px;
	}
</style>
