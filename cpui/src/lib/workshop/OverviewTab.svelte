<script lang="ts">
	// Workshop overview tab (DESIGN 7.7): live runs as on the desk, then a
	// read-only summary of the setup — repositories, commands, default flow,
	// egress — each pane linking to its section of the setup form.
	import type { Flow, Integration, Project, Run } from '$lib/domain';
	import { to } from '$lib/guild';
	import { plural, stateTab } from '$lib/format';
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
				<h2>{live.length ? 'Brewing now' : 'All still on the shelf'}</h2>
				{#if live.length}<span class="count">{live.length}</span>{/if}
				<span class="spacer"></span>
				<a class="small mut" href={to.workshop(project.id, 'runs')}>All runs {I.forward}</a>
			</div>
			{#each live as r (r.id)}<RunRow run={r} />{/each}
			{#if !live.length}
				<div class="pane-body row">
					<span class="mut small">Runs start from tickets.</span>
					<a class="btn btn-sm" href={to.newTicket({ project: project.id })}>{I.add} New ticket</a>
				</div>
			{/if}
			{#if finished.length}
				<div class="pane-sub fin">Recently finished</div>
				{#each finished as r (r.id)}<RunRow run={r} variant="finished" />{/each}
			{/if}
		</section>

		<section class="c pane">
			<div class="pane-head">
				<h2>Repositories</h2>
				<span class="count">{1 + (project.repos?.length ?? 0)}</span>
				<span class="spacer"></span>
				<a class="small mut" href={settings('repository')}>Edit</a>
			</div>
			<div class="pane-body col">
				<div class="repo">
					<span class="mono nm">repo <span class="hue-brand" title="primary">{I.star}</span></span>
					<span class="ell mono url" title={project.repo.url}>{project.repo.url}</span>
					<span class="mono mut">{I.branch} {project.repo.defaultBranch}</span>
				</div>
				<dl class="kv">
					<dt>locator</dt>
					<dd class="mono">{project.repo.locator || '—'}</dd>
					<dt>guide</dt>
					<dd class="mono">{project.repo.agentsMdPath || '—'}</dd>
					<dt>forge</dt>
					<dd>{intName(project.forgeId)}</dd>
				</dl>
				{#each project.repos ?? [] as r (r.name)}
					<div class="repo">
						<span class="mono nm">{r.name}</span>
						<span class="ell mono url" title={r.url}>{r.url}</span>
						<span class="mono mut"
							>{I.branch}
							{r.defaultBranch}{#if !r.locator}
								<span class="pill sm t-mut">read-only</span>{/if}</span
						>
					</div>
				{/each}
			</div>
			{#if project.repos?.length}
				<div class="pane-foot">
					<span
						>Cloned to <code>~/repos/&lt;name&gt;</code> beside the primary, one merge request each.</span
					>
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
				<div class="pane-body">
					<dl class="kv cmds">
						{#each commands as [k, v] (k)}
							<dt class="mono">
								{k}{#if k === 'setup'}<span class="pill sm t-grn">bootstrap</span>{/if}
							</dt>
							<dd><code class="script">{v}</code></dd>
						{/each}
					</dl>
				</div>
				<div class="pane-foot">
					<span
						>Flows run these by name with <code>run_ref</code>; <code>setup</code> runs when a workspace
						boots.</span
					>
				</div>
			{:else}
				<p class="pane-body lede">
					No commands — workspace setup is skipped, and flows that use <code>run_ref</code> fail preflight.
				</p>
			{/if}
		</section>

		<section class="c pane">
			<div class="pane-head">
				<h2>The formula</h2>
				<span class="spacer"></span>
				<a class="small mut" href={to.workshop(project.id, 'flows')}
					>{plural(ownCount, 'flow')} {I.forward}</a
				>
			</div>
			<div class="pane-body row fl">
				<span class="mut">Runs start on</span>
				<a class="tag" href={to.flow(project.id, project.defaultFlow)}
					>{project.defaultFlow || '—'}</a
				>
				{#if defaultFlow?.builtin}<span class="pill sm t-mut">built-in</span>{/if}
				{#if defaultFlow?.shadowsBuiltin}<span class="pill sm t-mut">from template</span>{/if}
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
			<div class="pane-body col">
				<p class="lede">
					{deny
						? 'Allow everything except the hosts below.'
						: 'Deny everything except the hosts below, plus what the forge and harness need.'}
				</p>
				{#if domains.length}
					<div class="chips">
						{#each showAll ? domains : domains.slice(0, CAP) as d (d)}
							<span class="pill sm mono {deny ? 't-ros' : 't-mut'}">{d}</span>
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
					<div class="lbl">Force-blocked · from the run’s Egress window</div>
					<div class="chips">
						{#each blocked as d (d)}<span class="pill sm mono t-ros">{d}</span>{/each}
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
			<div class="pane-body">
				<dl class="kv">
					<dt>provisioner</dt>
					<dd>{intName(project.provisionerId)}</dd>
					<dt>harnesses</dt>
					<dd>{(project.harnessIds ?? []).map(intName).join(', ') || '—'}</dd>
					<dt>extensions</dt>
					<dd class="mono">{(project.workspace.codeServerExtensions ?? []).join(', ') || '—'}</dd>
					<dt>editor</dt>
					<dd>{editorBits.join(' · ') || 'code-server defaults'}</dd>
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
