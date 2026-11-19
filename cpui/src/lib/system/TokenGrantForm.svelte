<script lang="ts" module>
	/** The kinds of action the catalogue names; another kind is headed by its
	 *  own word. */
	const KINDS = [
		'instance',
		'account',
		'project',
		'run',
		'ticket',
		'integration',
		'credential',
		'channel',
		'skill',
		'analytics',
		'notice',
		'settings',
		'assistant'
	] as const;
	type Kind = (typeof KINDS)[number];
	const known = (k: string): k is Kind => (KINDS as readonly string[]).includes(k);

	type Mode = 'full' | 'mcp' | 'limited';
	const HELP = {
		full: 'page.account.accessFullHelp',
		mcp: 'page.account.accessMcpHelp',
		limited: 'page.account.accessLimitedHelp'
	} as const satisfies Record<Mode, string>;
</script>

<script lang="ts">
	// Mints a personal token: its name, and what it may do. Full access is
	// the account itself; a limited token holds only the actions ticked
	// (what the engine says a token may hold, never account:manage), in the
	// workshops ticked (none = every one). An MCP client's token holds the
	// read actions the engine's MCP tools need, as the engine marks them.
	import { api } from '$lib/api';
	import { fleet } from '$lib/app/fleet.svelte';
	import { errorMessage } from '$lib/core/errors';
	import { I } from '$lib/design/icons';
	import type { Permission, Token, TokenGrant } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import Checkbox from '$lib/ui/Checkbox.svelte';
	import CheckboxGroup from '$lib/ui/CheckboxGroup.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';

	let {
		oncreated
	}: {
		/** The new token and its secret, shown once; mcp when it was minted
		 *  for an MCP client. */
		oncreated: (minted: { token: Token; secret: string; mcp: boolean }) => void | Promise<void>;
	} = $props();

	let name = $state('');
	let mode = $state<Mode>('full');
	let actions = $state<string[]>([]);
	let projectIds = $state<string[]>([]);
	let busy = $state(false);
	let error = $state('');

	/** What a token may hold, by kind in the catalogue's order; null until
	 *  read. */
	let groups = $state<{ kind: string; list: Permission[] }[] | null>(null);
	/** The actions the engine's MCP tools need; null until read. */
	let mcpActions = $state<string[] | null>(null);
	let permsError = $state('');

	const workshops = $derived(fleet.projects.map((p) => ({ value: p.id, label: p.name })));
	const grant = $derived<TokenGrant>(
		mode === 'full'
			? { full: true }
			: {
					full: false,
					actions: mode === 'mcp' ? [...(mcpActions ?? [])] : [...actions],
					projectIds: [...projectIds]
				}
	);
	const valid = $derived(
		mode === 'full' || (mode === 'mcp' ? !!mcpActions?.length : actions.length > 0)
	);

	async function loadPerms() {
		permsError = '';
		try {
			const by = new Map<string, Permission[]>();
			const tools: string[] = [];
			for (const p of await api.auth.permissions()) {
				if (!p.grantable || p.action === 'account:manage') continue;
				by.set(p.kind, [...(by.get(p.kind) ?? []), p]);
				if (p.mcp) tools.push(p.action);
			}
			groups = [...by].map(([kind, list]) => ({ kind, list }));
			mcpActions = tools;
		} catch (err) {
			permsError = errorMessage(err);
		}
	}

	function choose(m: Mode) {
		if (m !== 'full' && !groups) void loadPerms();
	}

	function tick(action: string, on: boolean) {
		actions = on ? [...actions, action] : actions.filter((a) => a !== action);
	}

	async function mint(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			const minted = await api.auth.createToken(name.trim(), grant);
			const mcp = mode === 'mcp';
			name = '';
			mode = 'full';
			actions = [];
			projectIds = [];
			await oncreated({ ...minted, mcp });
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}
</script>

{#snippet workshopPicker()}
	{#if workshops.length}
		<fieldset class="field">
			<legend>{t('page.account.workshops')}</legend>
			<CheckboxGroup row options={workshops} bind:value={projectIds} />
			<div class="help">{t('page.account.workshopsHelp')}</div>
		</fieldset>
	{/if}
{/snippet}

<form class="col" onsubmit={mint}>
	<div class="field">
		<label for="ac-token">{t('page.account.tokenName')}</label>
		<input
			id="ac-token"
			bind:value={name}
			required
			placeholder={t('page.account.tokenNamePlaceholder')}
		/>
	</div>
	<div class="field">
		<span class="field-label">{t('page.account.access')}</span>
		<Segmented
			size="sm"
			glass={false}
			label={t('page.account.access')}
			bind:value={mode}
			onchange={choose}
			options={[
				{ value: 'full', label: t('page.account.accessFull') },
				{ value: 'mcp', label: t('page.account.accessMcp') },
				{ value: 'limited', label: t('page.account.accessLimited') }
			]}
		/>
		<div class="help">{t(HELP[mode])}</div>
	</div>
	{#if mode === 'mcp'}
		{#if permsError}
			<ErrorCard
				cause={t('page.account.permissionsFailed')}
				detail={permsError}
				onretry={loadPerms}
			/>
		{:else if !mcpActions}
			<Skeleton lines={1} />
		{:else}
			<div class="help mono">{mcpActions.join(' · ')}</div>
			{@render workshopPicker()}
		{/if}
	{:else if mode === 'limited'}
		{#if permsError}
			<ErrorCard
				cause={t('page.account.permissionsFailed')}
				detail={permsError}
				onretry={loadPerms}
			/>
		{:else if !groups}
			<Skeleton lines={3} />
		{:else}
			<div class="kinds">
				{#each groups as g (g.kind)}
					<fieldset class="field">
						<legend>{known(g.kind) ? t(`page.account.kind.${g.kind}`) : g.kind}</legend>
						<div class="choices">
							{#each g.list as p (p.action)}
								<Checkbox
									label={p.action}
									detail={p.doc}
									checked={actions.includes(p.action)}
									onchange={(on) => tick(p.action, on)}
								/>
							{/each}
						</div>
					</fieldset>
				{/each}
			</div>
			{#if !actions.length}<div class="help">{t('page.account.actionsHelp')}</div>{/if}
			{@render workshopPicker()}
		{/if}
	{/if}
	{#if error}<div class="error">{error}</div>{/if}
	<div class="form-actions">
		<button class="btn-primary" type="submit" disabled={busy || !valid} aria-busy={busy}
			>{`${I.add} ${t('page.account.mint')}`}</button
		>
	</div>
</form>

<style>
	.kinds {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
		gap: 14px 20px;
	}
</style>
