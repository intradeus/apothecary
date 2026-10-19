<script lang="ts">
	import { t, th } from '$lib/i18n/index.svelte';
	// Workshop overview tab (DESIGN 7.7): live runs as on the desk, then a
	// read-only summary of the setup — repositories, commands, default flow,
	// egress — each pane linking to its section of the setup form.
	import type { Flow, Integration, Project, Run } from '$lib/domain';
	import { to } from '$lib/guild';
	import { stateTab } from '$lib/format';
	import { I } from '$lib/ui/icons';
	import RunRow from '$lib/run/RunRow.svelte';

	let {
		project,
		integrations,
		flows,
		runs
	}: {
		project: Project;
		integrations: Integration[] | null;
		flows: Flow[] | null;
		runs: Run[];
	} = $props();

	const live = $derived(
		runs
			.filter((r) => stateTab[r.state] === 'active')
			.sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
	);
	const finished = $derived(
		runs
			.filter((r) => stateTab[r.state] === 'finished' && r.state !== 'archived')
			.sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
			.slice(0, 4)
	);

	const commands = $derived(
		Object.entries(project.commands).sort(([a], [b]) =>
			a === 'setup' ? -1 : b === 'setup' ? 1 : a.localeCompare(b)
		)
	);
	const defaultFlow = $derived(flows?.find((f) => f.name === project.defaultFlow));
	const ownCount = $derived((flows ?? []).filter((f) => !f.builtin).length);

	const deny = $derived(project.workspace.egressMode === 'deny');
	const domains = $derived(
		deny ? (project.workspace.deniedDomains ?? []) : (project.workspace.extraDomains ?? [])
	);
	const blocked = $derived(project.workspace.blockedDomains ?? []);
	let showAll = $state(false);
	const CAP = 18;

	const intName = (id: string) => integrations?.find((i) => i.id === id)?.name ?? '…';
	const editor = $derived(project.workspace.editor);
	const editorBits = $derived(
		[
			editor?.theme,
			editor?.fontSize ? t('workshop.overview.editorFont', { size: editor.fontSize }) : '',
			editor?.tabSize ? t('workshop.overview.editorTab', { size: editor.tabSize }) : '',
			editor?.formatOnSave ? t('workshop.overview.editorFormat') : '',
			editor?.wordWrap ? t('workshop.overview.editorWrap') : '',
			editor?.trustWorkspace ? t('workshop.overview.editorTrusted') : ''
		].filter(Boolean)
	);
	const settings = (anchor: string) => `${to.workshopSettings(project.id)}#${anchor}`;
</script>

<div class="ov">
	<div class="col">
		<section class="c pane">
			<div class="pane-head">
				<h2>{live.length ? t('workshop.overview.brewing') : t('workshop.overview.shelf')}</h2>
				{#if live.length}<span class="count">{live.length}</span>{/if}
				<span class="spacer"></span>
				<a class="small mut" href={to.workshop(project.id, 'runs')}
					>{t('workshop.overview.allRuns')} {I.forward}</a
				>
			</div>
			{#each live as r (r.id)}<RunRow run={r} />{/each}
			{#if !live.length}
				<div class="pane-body row">
					<span class="mut small">{t('workshop.overview.runsStart')}</span>
					<a class="btn btn-sm" href={to.newTicket({ project: project.id })}
						>{I.add} {t('workshop.overview.newTicket')}</a
					>
				</div>
			{/if}
			{#if finished.length}
				<div class="pane-sub fin">{t('workshop.overview.recent')}</div>
				{#each finished as r (r.id)}<RunRow run={r} variant="finished" />{/each}
			{/if}
		</section>

		<section class="c pane">
			<div class="pane-head">
				<h2>{t('workshop.overview.repos')}</h2>
				<span class="count">{1 + (project.repos?.length ?? 0)}</span>
				<span class="spacer"></span>
				<a class="small mut" href={settings('repository')}>{t('workshop.overview.edit')}</a>
			</div>
			<div class="pane-body col">
				<div class="repo">
					<span class="mono nm"
						>{t('workshop.overview.repoName')}
						<span class="hue-brand" title={t('workshop.overview.primary')}>{I.star}</span></span
					>
					<span class="ell mono url" title={project.repo.url}>{project.repo.url}</span>
					<span class="mono mut">{I.branch} {project.repo.defaultBranch}</span>
				</div>
				<dl class="kv">
					<dt>{t('workshop.overview.locator')}</dt>
					<dd class="mono">{project.repo.locator || '—'}</dd>
					<dt>{t('workshop.overview.guide')}</dt>
					<dd class="mono">{project.repo.agentsMdPath || '—'}</dd>
					<dt>{t('workshop.overview.forge')}</dt>
					<dd>{intName(project.forgeId)}</dd>
				</dl>
				{#each project.repos ?? [] as r (r.name)}
					<div class="repo">
						<span class="mono nm">{r.name}</span>
						<span class="ell mono url" title={r.url}>{r.url}</span>
						<span class="mono mut"
							>{I.branch}
							{r.defaultBranch}{#if !r.locator}
								<span class="pill sm t-mut">{t('workshop.overview.readOnly')}</span>{/if}</span
						>
					</div>
				{/each}
			</div>
			{#if project.repos?.length}
				<div class="pane-foot">
					<span>{@html th('workshop.overview.clonedTo')}</span>
				</div>
			{/if}
		</section>
	</div>

	<div class="col">
		<section class="c pane">
			<div class="pane-head">
				<h2>{t('workshop.overview.commands')}</h2>
				<span class="count">{commands.length}</span>
				<span class="spacer"></span>
				<a class="small mut" href={settings('workshop')}>{t('workshop.overview.edit')}</a>
			</div>
			{#if commands.length}
				<div class="pane-body">
					<dl class="kv cmds">
						{#each commands as [k, v] (k)}
							<dt class="mono">
								{k}{#if k === 'setup'}<span class="pill sm t-grn"
										>{t('workshop.overview.bootstrap')}</span
									>{/if}
							</dt>
							<dd><code class="script">{v}</code></dd>
						{/each}
					</dl>
				</div>
				<div class="pane-foot">
					<span>{@html th('workshop.overview.commandsNote')}</span>
				</div>
			{:else}
				<p class="pane-body lede">
					{@html th('workshop.overview.noCommands')}
				</p>
			{/if}
		</section>

		<section class="c pane">
			<div class="pane-head">
				<h2>{t('workshop.overview.formula')}</h2>
				<span class="spacer"></span>
				<a class="small mut" href={to.workshop(project.id, 'flows')}
					>{t('workshop.overview.flowsCount', { count: ownCount })} {I.forward}</a
				>
			</div>
			<div class="pane-body row fl">
				<span class="mut">{t('workshop.overview.runsStartOn')}</span>
				<a class="tag" href={to.flow(project.id, project.defaultFlow)}
					>{project.defaultFlow || '—'}</a
				>
				{#if defaultFlow?.builtin}<span class="pill sm t-mut">{t('workshop.overview.builtin')}</span
					>{/if}
				{#if defaultFlow?.shadowsBuiltin}<span class="pill sm t-mut"
						>{t('workshop.overview.fromTemplate')}</span
					>{/if}
				{#if defaultFlow}<span class="mono fnt">@{defaultFlow.hash.slice(0, 6)}</span>{/if}
				<span class="spacer"></span>
				<span class="fnt small">{t('workshop.overview.unlessPicked')}</span>
			</div>
		</section>

		<section class="c pane">
			<div class="pane-head">
				<h2>{t('workshop.overview.egress')}</h2>
				<span class="pill {deny ? 't-amb' : 't-grn'}"
					>{deny ? t('workshop.overview.denylist') : t('workshop.overview.allowlist')}</span
				>
				<span class="spacer"></span>
				<a class="small mut" href={settings('workspace')}>{t('workshop.overview.edit')}</a>
			</div>
			<div class="pane-body col">
				<p class="lede">
					{deny ? t('workshop.overview.denyLede') : t('workshop.overview.allowLede')}
				</p>
				{#if domains.length}
					<div class="chips">
						{#each showAll ? domains : domains.slice(0, CAP) as d (d)}
							<span class="pill sm mono {deny ? 't-ros' : 't-mut'}">{d}</span>
						{/each}
						{#if domains.length > CAP}
							<button class="btn-ghost btn-sm" onclick={() => (showAll = !showAll)}
								>{showAll
									? t('workshop.overview.showLess')
									: t('workshop.overview.more', { count: domains.length - CAP })}</button
							>
						{/if}
					</div>
				{:else}
					<div class="fnt small">
						{deny ? t('workshop.overview.nothingBlocked') : t('workshop.overview.noExtraHosts')}
					</div>
				{/if}
				{#if blocked.length}
					<div class="lbl">{t('workshop.overview.forceBlocked')}</div>
					<div class="chips">
						{#each blocked as d (d)}<span class="pill sm mono t-ros">{d}</span>{/each}
					</div>
				{/if}
			</div>
		</section>

		<section class="c pane">
			<div class="pane-head">
				<h2>{t('workshop.overview.workspace')}</h2>
				<span class="spacer"></span>
				<a class="small mut" href={settings('workspace')}>{t('workshop.overview.edit')}</a>
			</div>
			<div class="pane-body">
				<dl class="kv">
					<dt>{t('workshop.overview.provisioner')}</dt>
					<dd>{intName(project.provisionerId)}</dd>
					<dt>{t('workshop.overview.harnesses')}</dt>
					<dd>{(project.harnessIds ?? []).map(intName).join(', ') || '—'}</dd>
					<dt>{t('workshop.overview.extensions')}</dt>
					<dd class="mono">{(project.workspace.codeServerExtensions ?? []).join(', ') || '—'}</dd>
					<dt>{t('workshop.overview.editor')}</dt>
					<dd>{editorBits.join(' · ') || t('workshop.overview.editorDefaults')}</dd>
				</dl>
			</div>
		</section>
	</div>
</div>

<style>
	.ov {
		display: grid;
		grid-template-columns: minmax(0, 1.05fr) minmax(0, 1fr);
		gap: 12px;
		align-items: start;
	}
	.ov > .col {
		gap: 12px;
		min-width: 0;
	}
	.fin {
		border-top: 1px solid var(--cLn);
	}
	dl.kv {
		--k: 90px;
	}
	.repo {
		display: grid;
		grid-template-columns: 90px minmax(0, 1fr) auto;
		gap: 12px;
		align-items: center;
		font-size: var(--fs-label);
	}
	.nm {
		font-weight: 600;
	}
	.url {
		font-size: var(--fs-meta);
		color: var(--mut);
	}
	.cmds {
		--k: 120px;
		row-gap: 8px;
	}
	.cmds > dt {
		display: flex;
		align-items: center;
		gap: 6px;
		color: var(--tx);
		font-weight: 600;
	}
	.script {
		display: block;
		white-space: pre-wrap;
		word-break: break-word;
		font-size: var(--fs-help);
	}
	.fl {
		gap: 8px;
		font-size: var(--fs-ui);
	}
	@media (max-width: 1180px) {
		.ov {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
