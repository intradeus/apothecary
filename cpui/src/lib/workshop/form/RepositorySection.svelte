<script lang="ts">
	// 1 · Repository: the account, a browser of what it can see (picking one
	// fills the fields below, and the workshop's name if it has none yet), the
	// primary repository, the additional ones.
	import { errorMessage } from '$lib/core/errors';
	import { api } from '$lib/api';
	import type { ExtraRepo, Integration, Repo } from '$lib/domain';
	import { to } from '$lib/core/urls';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import SearchInput from '$lib/ui/SearchInput.svelte';
	import { I } from '$lib/design/icons';
	import { t, th } from '$lib/i18n/index.svelte';
	import FormPane from './FormPane.svelte';
	import ExtraRepos from './ExtraRepos.svelte';

	let {
		repoAccounts,
		repoAccountId = $bindable(),
		repoURL = $bindable(),
		locator = $bindable(),
		defaultBranch = $bindable(),
		agentsMD = $bindable(),
		name = $bindable(),
		extraRepos = $bindable()
	}: {
		repoAccounts: Integration[];
		repoAccountId: string;
		repoURL: string;
		locator: string;
		defaultBranch: string;
		agentsMD: string;
		name: string;
		extraRepos: ExtraRepo[];
	} = $props();

	const repoAccountOptions = $derived<SelectOption[]>(
		repoAccounts.map((f) => ({ value: f.id, label: `${f.name} (${f.kind})` }))
	);

	function addExtra(r?: Repo) {
		extraRepos = [
			...extraRepos,
			{
				name: r ? (r.name.split('/').pop() ?? r.name).toLowerCase() : '',
				url: r?.url ?? '',
				locator: r?.locator ?? '',
				defaultBranch: r?.defaultBranch || 'main'
			}
		];
	}

	// Repo picker.
	let repos = $state<Repo[] | null>(null);
	let repoQuery = $state('');
	let loadingRepos = $state(false);
	let repoError = $state('');

	const shownRepos = $derived(
		(repos ?? []).filter((r) => {
			const q = repoQuery.trim().toLowerCase();
			return !q || r.name.toLowerCase().includes(q) || r.url.toLowerCase().includes(q);
		})
	);

	async function loadRepos() {
		loadingRepos = true;
		repoError = '';
		repos = null;
		try {
			repos = await api.integrations.repos(repoAccountId);
		} catch (e) {
			repoError = errorMessage(e);
		} finally {
			loadingRepos = false;
		}
	}

	function pickRepo(r: Repo) {
		repoURL = r.url;
		locator = r.locator;
		if (r.defaultBranch) defaultBranch = r.defaultBranch;
		if (!name) name = r.name.split('/').pop() ?? r.name;
		repos = null;
	}
</script>

<FormPane
	id="repository"
	step={1}
	title={t('form.project.repoHead')}
	lead={t('form.project.repoSub')}
>
	<div class="grid2">
		<div class="field">
			<label for="p-repo-account">{t('form.project.repoAccount')}</label>
			<div class="row nowrap">
				<Select
					id="p-repo-account"
					options={repoAccountOptions}
					bind:value={repoAccountId}
					required
				/>
				<button
					type="button"
					class="btn-sel"
					onclick={loadRepos}
					disabled={!repoAccountId || loadingRepos}
					aria-busy={loadingRepos}
					>{loadingRepos ? t('form.project.loading') : t('form.project.browse')}</button
				>
			</div>
			{#if repoAccounts.length === 0}
				<div class="error">
					{@html th('form.project.noRepoAccount', { href: to.system('integrations') })}
				</div>
			{/if}
		</div>
	</div>

	{#if repoError}<ErrorCard
			cause={t('form.project.listFail')}
			detail={repoError}
			onretry={loadRepos}
		/>{/if}
	{#if repos}
		<div class="browser card-in materialize">
			<div class="row nowrap bh">
				<SearchInput
					bind:value={repoQuery}
					width={480}
					placeholder={t('form.project.filterRepos', { count: repos.length })}
				/>
				<span class="spacer"></span>
				<button type="button" class="btn-ghost btn-sm" onclick={() => (repos = null)}
					>{t('form.project.close')}</button
				>
			</div>
			<div class="blist">
				{#each shownRepos as r (r.locator)}
					<div class="br">
						<span class="truncate">{r.name}</span>
						<span class="mono faint">{r.defaultBranch}</span>
						<button type="button" class="btn-sm btn-sel" onclick={() => pickRepo(r)}
							>{t('form.project.use')}</button
						>
						<button
							type="button"
							class="btn-sm"
							title={t('form.project.addExtraTitle')}
							onclick={() => addExtra(r)}>{I.add} {t('form.project.extra')}</button
						>
					</div>
				{:else}
					<div class="empty-line">
						{repos.length ? t('form.project.noMatch') : t('form.project.noneVisible')}
					</div>
				{/each}
			</div>
		</div>
	{/if}

	<div class="grid2">
		<div class="field wide">
			<label for="p-url">{t('form.project.repoUrl')}</label>
			<input
				id="p-url"
				class="mono"
				bind:value={repoURL}
				required
				placeholder="https://gitlab.com/group/project.git"
			/>
		</div>
		<div class="field">
			<label for="p-locator">{t('form.project.locator')}</label>
			<input id="p-locator" bind:value={locator} required class="mono" />
			<div class="help">{t('form.project.locatorHelp')}</div>
		</div>
		<div class="field">
			<label for="p-branch">{t('form.project.defaultBranch')}</label>
			<input id="p-branch" class="mono" bind:value={defaultBranch} />
			<div class="help">{t('form.project.defaultBranchHelp')}</div>
		</div>
		<div class="field">
			<label for="p-agentsmd">{t('form.project.agentsMd')}</label>
			<input id="p-agentsmd" class="mono" bind:value={agentsMD} />
			<div class="help">{t('form.project.agentsMdHelp')}</div>
		</div>
	</div>

	<ExtraRepos bind:rows={extraRepos} onadd={() => addExtra()} />
</FormPane>

<style>
	.browser {
		overflow: hidden;
	}
	.bh {
		padding: 8px;
		border-bottom: 1px solid var(--line);
	}
	.blist {
		max-height: 250px;
		overflow: auto;
	}
	.br {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto auto auto;
		gap: 10px;
		align-items: center;
		padding: 6px 10px;
		border-top: 1px solid var(--line);
		font-size: var(--fs-ui);
	}
	.br:first-child {
		border-top: 0;
	}
	.br:hover {
		background: var(--fill-1);
	}
	.br .mono {
		font-size: var(--fs-meta);
	}
</style>
