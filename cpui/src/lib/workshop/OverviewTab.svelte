<script lang="ts">
	// Workshop overview tab (DESIGN 7.7): live runs as on the desk, then a
	// read-only summary of the setup — repositories, commands, default flow,
	// egress — each pane linking to its section of the setup form.
	import type { Flow, Integration, Project, Run } from '$lib/domain';
	import { to } from '$lib/guild';
	import { lore } from '$lib/lore';
	import { age, fmtAbs, plural, shortId, stateTab } from '$lib/format';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import LiveRunRow from './LiveRunRow.svelte';

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
			editor?.fontSize ? `font ${editor.fontSize}` : '',
			editor?.tabSize ? `tab ${editor.tabSize}` : '',
			editor?.formatOnSave ? 'format on save' : '',
			editor?.wordWrap ? 'word wrap' : '',
			editor?.trustWorkspace ? 'trusted' : ''
		].filter(Boolean)
	);
	const settings = (anchor: string) => `${to.workshopSettings(project.id)}#${anchor}`;
</script>

<div class="ov">
	<div class="col">
		<section class="c pane">
			<div class="pane-head">
				<h2>{live.length ? 'Brewing now' : lore('nothingRunning')}</h2>
				{#if live.length}<span class="count">{live.length}</span>{/if}
				<span class="spacer"></span>
				<a class="small mut" href={to.workshop(project.id, 'runs')}>All runs →</a>
			</div>
			{#each live as r (r.id)}<LiveRunRow run={r} />{/each}
			{#if !live.length}
				<div class="quiet">
					<span class="mut">Runs start from tickets.</span>
					<a class="btn btn-sm" href={to.newTicket({ project: project.id })}>+ New ticket</a>
				</div>
			{/if}
			{#if finished.length}
				<div class="sub-h">Recently finished</div>
				{#each finished as r (r.id)}
					<a class="fr" href={to.run(r.id)}>
						<span class="ell">{r.title || 'Untitled run'}</span>
						<StatusChip state={r.state} size="sm" />
						<span class="mono fnt id">{shortId(r.id)}</span>
						<span class="mono fnt" title={fmtAbs(r.updatedAt)}>{age(r.updatedAt)}</span>
					</a>
				{/each}
			{/if}
		</section>

		<section class="c pane">
			<div class="pane-head">
				<h2>Repositories</h2>
				<span class="count">{1 + (project.repos?.length ?? 0)}</span>
				<span class="spacer"></span>
				<a class="small mut" href={settings('repository')}>Edit</a>
			</div>
			<div class="rows">
				<div class="r rp">
					<span class="mono nm">repo <span class="star" title="primary">★</span></span>
					<span class="ell mono url" title={project.repo.url}>{project.repo.url}</span>
					<span class="mono mut">⎇ {project.repo.defaultBranch}</span>
				</div>
				<div class="r kv">
					<span class="mut">locator</span><span class="mono">{project.repo.locator || '—'}</span>
					<span class="mut">guide</span><span class="mono">{project.repo.agentsMdPath || '—'}</span>
					<span class="mut">forge</span><span>{intName(project.forgeId)}</span>
				</div>
				{#each project.repos ?? [] as r (r.name)}
					<div class="r rp">
						<span class="mono nm">{r.name}</span>
						<span class="ell mono url" title={r.url}>{r.url}</span>
						<span class="mono mut"
							>⎇ {r.defaultBranch}{#if !r.locator}<span class="pill t-mut ro">read-only</span
								>{/if}</span
						>
					</div>
				{/each}
			</div>
			{#if project.repos?.length}
				<div class="foot-note">
					Cloned to <code>~/repos/&lt;name&gt;</code> beside the primary, one merge request each.
				</div>
			{/if}
		</section>
	</div>

	<div class="col">
		<section class="c pane">
			<div class="pane-head">
				<h2>Commands</h2>
				<span class="count">{commands.length}</span>
				<span class="spacer"></span>
				<a class="small mut" href={settings('workshop')}>Edit</a>
			</div>
			{#if commands.length}
				<div class="rows">
					{#each commands as [k, v] (k)}
						<div class="r cmd">
							<span class="mono nm"
								>{k}{#if k === 'setup'}<span class="pill t-grn boot">bootstrap</span>{/if}</span
							>
							<code class="script">{v}</code>
						</div>
					{/each}
				</div>
				<div class="foot-note">
					Flows run these by name with <code>run_ref</code>; <code>setup</code> runs when a workspace
					boots.
				</div>
			{:else}
				<div class="pane-body mut small">
					No commands — workspace setup is skipped, and flows that use <code>run_ref</code> fail preflight.
				</div>
			{/if}
		</section>

		<section class="c pane">
			<div class="pane-head">
				<h2>{lore('flow')}</h2>
				<span class="spacer"></span>
				<a class="small mut" href={to.workshop(project.id, 'flows')}>{plural(ownCount, 'flow')} →</a
				>
			</div>
			<div class="pane-body fl">
				<span class="mut">Runs start on</span>
				<a class="tag big" href={to.flow(project.id, project.defaultFlow)}
					>{project.defaultFlow || '—'}</a
				>
				{#if defaultFlow?.builtin}<span class="pill t-mut">built-in</span>{/if}
				{#if defaultFlow?.shadowsBuiltin}<span class="pill t-mut">from template</span>{/if}
				{#if defaultFlow}<span class="mono fnt">@{defaultFlow.hash.slice(0, 6)}</span>{/if}
				<span class="spacer"></span>
				<span class="fnt small">unless a run picks another</span>
			</div>
		</section>

		<section class="c pane">
			<div class="pane-head">
				<h2>Egress</h2>
				<span class="pill {deny ? 't-amb' : 't-grn'}">{deny ? 'Denylist' : 'Allowlist'}</span>
				<span class="spacer"></span>
				<a class="small mut" href={settings('workspace')}>Edit</a>
			</div>
			<div class="pane-body eg">
				<div class="mut small">
					{deny
						? 'Allow everything except the hosts below.'
						: 'Deny everything except the hosts below, plus what the forge and harness need.'}
				</div>
				{#if domains.length}
					<div class="chips">
						{#each showAll ? domains : domains.slice(0, CAP) as d (d)}
							<span class="dm mono" class:den={deny}>{d}</span>
						{/each}
						{#if domains.length > CAP}
							<button class="btn-ghost btn-sm" onclick={() => (showAll = !showAll)}
								>{showAll ? 'Show less' : `+${domains.length - CAP} more`}</button
							>
						{/if}
					</div>
				{:else}
					<div class="fnt small">
						{deny ? 'Nothing blocked.' : 'No extra hosts — only the derived allowlist applies.'}
					</div>
				{/if}
				{#if blocked.length}
					<div class="sub-l">
						Force-blocked <span class="fnt">· from the run’s Egress window</span>
					</div>
					<div class="chips">
						{#each blocked as d (d)}<span class="dm mono den">{d}</span>{/each}
					</div>
				{/if}
			</div>
		</section>

		<section class="c pane">
			<div class="pane-head">
				<h2>Workspace</h2>
				<span class="spacer"></span>
				<a class="small mut" href={settings('workspace')}>Edit</a>
			</div>
			<div class="rows">
				<div class="r kv">
					<span class="mut">provisioner</span><span>{intName(project.provisionerId)}</span>
					<span class="mut">harnesses</span><span
						>{(project.harnessIds ?? []).map(intName).join(', ') || '—'}</span
					>
					<span class="mut">extensions</span><span class="mono ell"
						>{(project.workspace.codeServerExtensions ?? []).join(', ') || '—'}</span
					>
					<span class="mut">editor</span><span
						>{editorBits.join(' · ') || 'code-server defaults'}</span
					>
				</div>
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
	.col {
		display: flex;
		flex-direction: column;
		gap: 12px;
		min-width: 0;
	}
	.quiet {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 12px 16px;
		font-size: 12.5px;
	}
	.sub-h {
		padding: 10px 14px 4px;
		font-size: 11px;
		font-weight: 500;
		color: var(--mut);
		border-top: 1px solid var(--cLn);
	}
	.fr {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto 52px 36px;
		gap: 10px;
		align-items: center;
		padding: 7px 14px;
		border-top: 1px solid var(--cLn);
		font-size: 12.5px;
		color: var(--tx);
	}
	.fr:hover {
		background: var(--p1);
		text-decoration: none;
	}
	.fr .mono {
		font-size: 10.5px;
		text-align: right;
	}
	.rp {
		grid-template-columns: 90px minmax(0, 1fr) auto;
		font-size: 12px;
	}
	.kv {
		grid-template-columns: 90px minmax(0, 1fr);
		gap: 4px 12px;
		font-size: 12px;
	}
	.kv > :nth-child(odd) {
		font-size: 11.5px;
	}
	.nm {
		font-weight: 600;
		display: inline-flex;
		align-items: center;
		gap: 6px;
	}
	.star {
		color: var(--brand);
	}
	.url {
		font-size: 11px;
		color: var(--mut);
	}
	.ro {
		margin-left: 6px;
		font-size: 10px;
		padding: 0 6px;
	}
	.cmd {
		grid-template-columns: 130px minmax(0, 1fr);
		align-items: start;
	}
	.boot {
		font-size: 9.5px;
		padding: 0 6px;
	}
	.script {
		white-space: pre-wrap;
		word-break: break-word;
		font-size: 11.5px;
		display: block;
	}
	.foot-note {
		padding: 8px 16px 10px;
		border-top: 1px solid var(--cLn);
		font-size: 11.5px;
		color: var(--fnt);
	}
	.fl {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
		font-size: 12.5px;
	}
	.tag.big {
		font-size: 12px;
		padding: 1px 8px;
	}
	.eg {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
	}
	.dm {
		font-size: 10.5px;
		padding: 1px 8px;
		border-radius: 999px;
		background: var(--p2);
		border: 1px solid var(--ln);
	}
	.dm.den {
		background: var(--rosBg);
		border-color: var(--rosLn);
		color: var(--ros);
	}
	.sub-l {
		font-size: 11.5px;
		font-weight: 500;
		margin-top: 4px;
	}
	@media (max-width: 1100px) {
		.ov {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
