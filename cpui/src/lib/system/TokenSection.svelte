<script lang="ts">
	// The Account page's personal tokens: mint one (TokenGrantForm), its
	// secret shown once (with the MCP client configuration when it was minted
	// for one), and the list with what each may do, how long it lasts and its
	// revoke. The list is the tokens that still work; the ones past their
	// expiry (the engine keeps them until revoked) are a switch away.
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { fleet } from '$lib/app/fleet.svelte';
	import { errorMessage } from '$lib/core/errors';
	import { ago } from '$lib/core/format';
	import { I } from '$lib/design/icons';
	import type { Token } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import TokenCalls from './TokenCalls.svelte';
	import TokenGrantForm from './TokenGrantForm.svelte';
	import { tokenLife } from './tokenLife';

	let tokens = $state<Token[]>([]);
	let loaded = $state(false);
	let error = $state('');
	let minted = $state<{ token: Token; secret: string; mcp: boolean } | null>(null);
	let revoking = $state<string | null>(null);
	/** The token whose calls are open. */
	let open = $state<string | null>(null);

	/** The clock the tokens' lives are read off: a token whose time comes
	 *  while the page is open leaves the active ones. */
	const TICK_MS = 30_000;
	let now = $state(Date.now());
	onMount(() => {
		const h = setInterval(() => (now = Date.now()), TICK_MS);
		return () => clearInterval(h);
	});
	const listed = $derived(tokens.map((tk) => ({ tk, life: tokenLife(tk, now) })));
	const active = $derived(listed.filter((x) => !x.life.expired));
	const expired = $derived(listed.filter((x) => x.life.expired));
	let view = $state<'active' | 'expired'>('active');
	const shown = $derived(view === 'expired' ? expired : active);
	/** What an empty list says: no token at all, or none on this side. */
	const none = $derived(
		!tokens.length ? 'noTokens' : view === 'expired' ? 'noExpired' : 'noActive'
	);

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

	/** The engine's MCP server, as this browser reaches it. */
	const mcpURL = `${location.origin}/mcp`;

	/** The configuration most MCP clients take for a remote server. */
	const mcpConfig = (secret: string) =>
		JSON.stringify(
			{
				mcpServers: {
					apothecary: {
						type: 'http',
						url: mcpURL,
						headers: { Authorization: `Bearer ${secret}` }
					}
				}
			},
			null,
			2
		);

	async function created(m: { token: Token; secret: string; mcp: boolean }) {
		minted = m;
		// The new token is an active one: it is listed whatever was shown.
		view = 'active';
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
					{#if minted.mcp}
						{@const config = mcpConfig(secret)}
						<span class="muted small">{t('page.account.mcpConnect', { url: mcpURL })}</span>
						<div class="row nowrap">
							<pre class="config mono">{config}</pre>
							<button class="btn-sm" onclick={() => copySecret(config)}
								>{t('page.account.copy')}</button
							>
						</div>
					{:else}
						<span class="muted small">{t('page.account.usage', { secret })}</span>
					{/if}
				</div>
			</Callout>
		{/if}
		{#if error}<ErrorCard cause={t('page.account.failed')} detail={error} />{/if}
		{#if !loaded}
			<Skeleton lines={2} />
		{:else if tokens.length}
			<div class="row">
				<Segmented
					options={[
						{ value: 'active', label: t('page.account.showActive'), count: active.length },
						{ value: 'expired', label: t('page.account.showExpired'), count: expired.length }
					]}
					bind:value={view}
					size="sm"
					glass={false}
					label={t('page.account.show')}
				/>
			</div>
		{/if}
		{#if loaded && shown.length}
			<div class="rows">
				{#each shown as { tk, life } (tk.id)}
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
						<span class="when">
							<span class="muted small truncate">
								{t('page.account.created', { ago: ago(tk.createdAt, now) })}
								· {tk.lastUsedAt
									? t('page.account.lastUsed', { ago: ago(tk.lastUsedAt, now) })
									: t('page.account.neverUsed')}
							</span>
							<span class="muted small truncate" title={life.at || undefined}>{life.text}</span>
						</span>
						<span class="row nowrap">
							<button
								class="btn-sm"
								aria-expanded={open === tk.id}
								title={t('page.account.callsTitle')}
								onclick={() => (open = open === tk.id ? null : tk.id)}
								>{t('page.account.calls')}</button
							>
							<button
								class="btn-danger btn-sm"
								aria-busy={revoking === tk.id}
								onclick={() => revoke(tk)}>{t('page.account.revoke')}</button
							>
						</span>
					</div>
					{#if open === tk.id}<TokenCalls tokenId={tk.id} />{/if}
				{/each}
			</div>
		{:else if loaded}
			<EmptyState compact title={t(`page.account.${none}`)} text={t(`page.account.${none}Text`)} />
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
	.config {
		flex: 1;
		min-width: 0;
		margin: 0;
		overflow-x: auto;
		user-select: all;
		font-size: var(--fs-label);
	}
	.trow {
		grid-template-columns: minmax(0, 1fr) 110px minmax(0, 1.4fr) auto;
		align-items: center;
	}
	.who,
	.when {
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
