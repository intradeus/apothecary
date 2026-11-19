<script lang="ts">
	// The Account page's personal tokens: mint one (TokenGrantForm), its
	// secret shown once, and the list with what each may do and its revoke.
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { fleet } from '$lib/app/fleet.svelte';
	import { errorMessage } from '$lib/core/errors';
	import { ago } from '$lib/core/format';
	import { I } from '$lib/design/icons';
	import type { Token } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import TokenGrantForm from './TokenGrantForm.svelte';
	import { tokenLife } from './tokenLife';

	let tokens = $state<Token[]>([]);
	let loaded = $state(false);
	let error = $state('');
	let minted = $state<{ token: Token; secret: string } | null>(null);
	let revoking = $state<string | null>(null);

	const load = async () => {
		try {
			tokens = await api.auth.tokens();
			loaded = true;
			error = '';
		} catch (err) {
			error = errorMessage(err);
		}
	};
	onMount(load);

	async function created(m: { token: Token; secret: string }) {
		minted = m;
		await load();
	}

	/** "Full access", or "3 actions · all workshops" / "· billing, web". */
	function scope(tk: Token): string {
		if (!tk.actions) return t('page.account.accessFull');
		const where = tk.projectIds
			? tk.projectIds.map((id) => fleet.projectName(id)).join(', ')
			: t('page.account.scopeAllWorkshops');
		return `${t('page.account.scopeActions', { count: tk.actions.length })} · ${where}`;
	}

	async function copySecret(secret: string) {
		try {
			await navigator.clipboard.writeText(secret);
			toast(t('page.account.copied'));
		} catch {
			/* no clipboard: the secret is on screen to select */
		}
	}

	async function revoke(tk: Token) {
		const ok = await confirmDialog({
			title: t('page.account.revoke') + ` · ${tk.name}`,
			body: tk.prefix + '…',
			action: t('page.account.revoke')
		});
		if (!ok) return;
		revoking = tk.id;
		try {
			await api.auth.revokeToken(tk.id);
			if (minted?.token.id === tk.id) minted = null;
			toast(t('page.account.revoked'));
			await load();
		} catch (err) {
			error = errorMessage(err);
		} finally {
			revoking = null;
		}
	}
</script>

<section class="glass pane form-dense">
	<div class="pane-head">
		<h2>{t('page.account.tokens')}</h2>
		<span class="muted small">{t('page.account.tokensSub')}</span>
	</div>
	<div class="pane-body col">
		<TokenGrantForm oncreated={created} />
		{#if minted}
			{@const secret = minted.secret}
			<Callout hue="attention" glyph={I.info} ondismiss={() => (minted = null)}>
				<div class="col">
					<strong>{t('page.account.minted')}</strong>
					<div class="row nowrap">
						<code class="secret mono">{secret}</code>
						<button class="btn-sm" onclick={() => copySecret(secret)}
							>{t('page.account.copy')}</button
						>
					</div>
					<span class="muted small">{t('page.account.usage', { secret })}</span>
				</div>
			</Callout>
		{/if}
		{#if error}<ErrorCard cause={t('page.account.failed')} detail={error} />{/if}
		{#if !loaded}
			<Skeleton lines={2} />
		{:else if tokens.length}
			<div class="rows">
				{#each tokens as tk (tk.id)}
					{@const life = tokenLife(tk)}
					<div class="r trow">
						<span class="who">
							<span class="nm truncate"
								>{#if life.cli}<span
										class="pill sm tint-accent-2"
										title={t('page.account.cliTitle')}>{t('page.account.cli')}</span
									>
								{/if}{tk.name}</span
							>
							<span class="muted small truncate">{scope(tk)}</span>
						</span>
						<span class="mono muted">{`${tk.prefix}…`}</span>
						<span class="muted small truncate">
							{t('page.account.created', { ago: ago(tk.createdAt) })}
							· {tk.lastUsedAt
								? t('page.account.lastUsed', { ago: ago(tk.lastUsedAt) })
								: t('page.account.neverUsed')}{#if life.text}
								{` · ${life.text}`}{/if}
						</span>
						<button
							class="btn-danger btn-sm"
							aria-busy={revoking === tk.id}
							onclick={() => revoke(tk)}>{t('page.account.revoke')}</button
						>
					</div>
				{/each}
			</div>
		{:else}
			<EmptyState
				compact
				title={t('page.account.noTokens')}
				text={t('page.account.noTokensText')}
			/>
		{/if}
	</div>
</section>

<style>
	.secret {
		flex: 1;
		min-width: 0;
		overflow-wrap: anywhere;
		user-select: all;
		font-size: var(--fs-label);
	}
	.trow {
		grid-template-columns: minmax(0, 1fr) 110px minmax(0, 1.4fr) auto;
		align-items: center;
	}
	.who {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.nm {
		font-weight: 600;
	}
	@media (max-width: 760px) {
		.trow {
			grid-template-columns: minmax(0, 1fr) auto;
		}
		.trow > :nth-child(2),
		.trow > :nth-child(3) {
			display: none;
		}
	}
</style>
