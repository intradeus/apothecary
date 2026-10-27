<script lang="ts">
	// One's account (TODO 25): the profile, the password, and personal API
	// tokens. Everything on this page is the signed-in user's own.
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { session } from '$lib/session.svelte';
	import type { Token } from '$lib/domain';
	import { ago } from '$lib/format';
	import { t } from '$lib/i18n/index.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { I } from '$lib/ui/icons';

	// Profile.
	let handle = $state(session.user?.handle ?? '');
	let email = $state(session.user?.email ?? '');
	let name = $state(session.user?.name ?? '');
	let profileBusy = $state(false);
	let profileError = $state('');

	async function saveProfile(e: SubmitEvent) {
		e.preventDefault();
		profileBusy = true;
		profileError = '';
		try {
			await api.auth.updateMe({ handle: handle.trim(), email: email.trim(), name: name.trim() });
			await session.refresh();
			toast(t('page.account.saved'));
		} catch (err) {
			profileError = err instanceof Error ? err.message : String(err);
		} finally {
			profileBusy = false;
		}
	}

	// Password.
	let current = $state('');
	let next = $state('');
	let again = $state('');
	let pwBusy = $state(false);
	let pwError = $state('');

	async function changePassword(e: SubmitEvent) {
		e.preventDefault();
		if (next !== again) {
			pwError = t('auth.setup.mismatch');
			return;
		}
		pwBusy = true;
		pwError = '';
		try {
			await api.auth.changePassword(current, next);
			current = next = again = '';
			toast(t('page.account.changed'));
		} catch (err) {
			pwError = err instanceof Error ? err.message : String(err);
		} finally {
			pwBusy = false;
		}
	}

	// Tokens.
	let tokens = $state<Token[]>([]);
	let tokensLoaded = $state(false);
	let tokensError = $state('');
	let tokenName = $state('');
	let minting = $state(false);
	let minted = $state<{ token: Token; secret: string } | null>(null);
	let revoking = $state<string | null>(null);

	const loadTokens = async () => {
		try {
			tokens = await api.auth.tokens();
			tokensLoaded = true;
			tokensError = '';
		} catch (err) {
			tokensError = err instanceof Error ? err.message : String(err);
		}
	};
	onMount(loadTokens);

	async function mint(e: SubmitEvent) {
		e.preventDefault();
		minting = true;
		tokensError = '';
		try {
			minted = await api.auth.createToken(tokenName.trim());
			tokenName = '';
			await loadTokens();
		} catch (err) {
			tokensError = err instanceof Error ? err.message : String(err);
		} finally {
			minting = false;
		}
	}

	async function copySecret() {
		if (!minted) return;
		try {
			await navigator.clipboard.writeText(minted.secret);
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
			await loadTokens();
		} catch (err) {
			tokensError = err instanceof Error ? err.message : String(err);
		} finally {
			revoking = null;
		}
	}
</script>

<section class="c pane form-dense">
	<div class="pane-head">
		<h2>{t('page.account.profile')}</h2>
		<span class="mut small">{t('page.account.profileSub')}</span>
	</div>
	<form class="pane-body col" onsubmit={saveProfile}>
		<div class="grid2">
			<div class="field">
				<label for="ac-handle">{t('auth.handle')}</label>
				<input id="ac-handle" class="mono" bind:value={handle} required autocomplete="username" />
				<div class="help">{t('auth.handleHelp')}</div>
			</div>
			<div class="field">
				<label for="ac-name">{t('auth.name')}</label>
				<input id="ac-name" bind:value={name} autocomplete="name" />
			</div>
		</div>
		<div class="field">
			<label for="ac-email">{t('auth.email')}</label>
			<input id="ac-email" type="email" bind:value={email} required autocomplete="email" />
		</div>
		<div class="help">
			{t('page.account.methods', { methods: session.me?.methods.join(', ') ?? '' })}
		</div>
		{#if profileError}<div class="err">{profileError}</div>{/if}
		<div class="form-actions">
			<button class="btn-primary" type="submit" disabled={profileBusy} aria-busy={profileBusy}
				>{t('page.account.save')}</button
			>
		</div>
	</form>
</section>

<section class="c pane form-dense">
	<div class="pane-head">
		<h2>{t('page.account.password')}</h2>
		<span class="mut small">{t('page.account.passwordSub')}</span>
	</div>
	<form class="pane-body col" onsubmit={changePassword}>
		<div class="field">
			<label for="ac-cur">{t('page.account.current')}</label>
			<input id="ac-cur" type="password" bind:value={current} required autocomplete="current-password" />
		</div>
		<div class="grid2">
			<div class="field">
				<label for="ac-new">{t('page.account.new')}</label>
				<input id="ac-new" type="password" bind:value={next} required minlength="8" autocomplete="new-password" />
				<div class="help">{t('auth.passwordHelp')}</div>
			</div>
			<div class="field">
				<label for="ac-again">{t('auth.passwordAgain')}</label>
				<input id="ac-again" type="password" bind:value={again} required autocomplete="new-password" />
			</div>
		</div>
		{#if pwError}<div class="err">{pwError}</div>{/if}
		<div class="form-actions">
			<button class="btn-primary" type="submit" disabled={pwBusy} aria-busy={pwBusy}
				>{t('page.account.change')}</button
			>
		</div>
	</form>
</section>

<section class="c pane form-dense">
	<div class="pane-head">
		<h2>{t('page.account.tokens')}</h2>
		<span class="mut small">{t('page.account.tokensSub')}</span>
	</div>
	<div class="pane-body col">
		<form class="row nowrap mint" onsubmit={mint}>
			<div class="field grow">
				<label for="ac-token">{t('page.account.tokenName')}</label>
				<input
					id="ac-token"
					bind:value={tokenName}
					required
					placeholder={t('page.account.tokenNamePlaceholder')}
				/>
			</div>
			<button class="btn-primary" type="submit" disabled={minting} aria-busy={minting}
				>{I.add} {t('page.account.mint')}</button
			>
		</form>
		{#if minted}
			<Callout hue="amb" glyph={I.info} ondismiss={() => (minted = null)}>
				<div class="col">
					<strong>{t('page.account.minted')}</strong>
					<div class="row nowrap">
						<code class="secret mono">{minted.secret}</code>
						<button class="btn-sm" onclick={copySecret}>{t('page.account.copy')}</button>
					</div>
					<span class="mut small">{t('page.account.usage', { secret: minted.secret })}</span>
				</div>
			</Callout>
		{/if}
		{#if tokensError}<ErrorCard cause={t('page.account.failed')} detail={tokensError} />{/if}
		{#if !tokensLoaded}
			<Skeleton lines={2} />
		{:else if tokens.length}
			<div class="rows">
				{#each tokens as tk (tk.id)}
					<div class="r trow">
						<span class="nm ell">{tk.name}</span>
						<span class="mono mut">{tk.prefix}…</span>
						<span class="mut small ell">
							{t('page.account.created', { ago: ago(tk.createdAt) })}
							· {tk.lastUsedAt
								? t('page.account.lastUsed', { ago: ago(tk.lastUsedAt) })
								: t('page.account.neverUsed')}
						</span>
						<button class="btn-danger btn-sm" aria-busy={revoking === tk.id} onclick={() => revoke(tk)}
							>{t('page.account.revoke')}</button
						>
					</div>
				{/each}
			</div>
		{:else}
			<EmptyState compact title={t('page.account.noTokens')} text={t('page.account.noTokensText')} />
		{/if}
	</div>
</section>

<style>
	.mint {
		align-items: flex-end;
		gap: 10px;
	}
	.mint .field {
		margin: 0;
	}
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
