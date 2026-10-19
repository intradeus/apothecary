<script lang="ts">
	// Flow editor (DESIGN 7.8): header (name @hash, Graph | YAML, Validate,
	// Versions, Clone, Delete… / Reset, Save), then the canvas beside a 340px
	// inspector. All document logic lives in flowdoc / FlowGraph /
	// FlowInspector; this page wires them up.
	import { onMount } from 'svelte';
	import { beforeNavigate, goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api } from '$lib/api';
	import type { ActionSpec, Flow, FlowVersion, SignalSpec } from '$lib/domain';
	import { fleet } from '$lib/fleet.svelte';
	import { currentGuild, to } from '$lib/guild';
	import { ago, fmtAbs } from '$lib/format';
	import { t, th } from '$lib/i18n/index.svelte';
	import FlowGraph, { type Selection } from '$lib/FlowGraph.svelte';
	import FlowInspector from '$lib/FlowInspector.svelte';
	import NameFlowDialog from '$lib/flow/NameFlowDialog.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import GlassGroup from '$lib/ui/GlassGroup.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import CheckChip from '$lib/ui/CheckChip.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/ui/icons';
	import { slide } from '$lib/ui/motion';
	import {
		ABORT,
		chainLinks,
		DEBUG,
		autoLayout,
		docToYaml,
		DONE,
		START,
		edgeOf,
		setEdge,
		setEntry,
		type EdgeKind,
		type FlowDoc
	} from '$lib/flowdoc';

	let flow = $state<Flow | null>(null);
	let yaml = $state('');
	let doc = $state<FlowDoc | null>(null);
	let layout = $state<Record<string, { x: number; y: number }>>({});
	let rev = $state(0);
	let tab = $state<'graph' | 'yaml'>('graph');
	let graphDirty = $state(false);
	let selection = $state<Selection>(null);
	let versions = $state<FlowVersion[]>([]);
	let error = $state('');
	let busy = $state(false);
	/** The last validation (or save) result, shown in the Validate popover. */
	let validation = $state<{
		ok: boolean;
		title: string;
		stages?: number;
		hash?: string;
		errors?: string[];
		warnings?: string[];
	} | null>(null);
	/** A one-line note over the canvas (a version loaded into the editor). */
	let notice = $state<{ tone: 'ok' | 'info'; text: string } | null>(null);
	/** A validation request is in flight (aria-busy on Validate). */
	let validating = $state(false);
	// The compiled-in action registry: what an `action` stage may name, and the
	// signature the inspector and the dataflow layer bind against.
	let actions = $state<ActionSpec[]>([]);
	// The signal registry: what a wait stage blocks on, or a gate auto-answers on.
	let signals = $state<SignalSpec[]>([]);

	let validateBtn = $state<HTMLButtonElement | null>(null);
	let validateOpen = $state(false);
	let showVersions = $state(false);
	let cloning = $state(false);
	let inspector = $state<ReturnType<typeof FlowInspector> | null>(null);

	// The workshop that owns this flow. A flow's stages name that workshop's
	// commands and its agents know that repository, so the editor is always
	// editing one workshop's document — never a shared one.
	const projectId = $derived(page.params.workshop!);
	const workshopName = $derived(fleet.projectById.get(projectId)?.name ?? '…');
	const guild = $derived(currentGuild());

	// The document itself is still validated on its own terms: agent, harness,
	// model and run_ref are free text here, checked against the workshop's
	// configuration at run creation (preflight).

	const dirty = $derived(flow !== null && (graphDirty || yaml !== flow.yaml));

	const load = async () => {
		error = '';
		try {
			flow = await api.flows.get(projectId, page.params.name!);
			yaml = flow.yaml;
			versions = await api.flows.versions(projectId, flow.name);
			const r = await api.flows.validate(flow.yaml);
			if (r.ok && r.doc) {
				doc = r.doc;
				layout = flow.layout?.nodes ?? autoLayout(doc);
				if (r.warnings?.length)
					validation = {
						ok: true,
						title: t('page.flow.validWarn'),
						stages: r.stages,
						hash: r.hash,
						warnings: r.warnings
					};
				rev++;
			} else {
				tab = 'yaml';
				validation = { ok: false, title: t('page.flow.noParse'), errors: r.errors ?? [] };
				validateOpen = true;
			}
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
		// Contracts for the binding fields and the dataflow view. Best-effort:
		// without them the graph still renders explicit bindings.
		try {
			[actions, signals] = await Promise.all([api.system.actions(), api.system.signals()]);
		} catch {
			/* binding hints stay off */
		}
	};
	onMount(load);

	function currentYAML(): string {
		if (doc && (tab === 'graph' || graphDirty)) return docToYaml(doc);
		return yaml;
	}

	function markChanged() {
		graphDirty = true;
		notice = null;
		rev++;
	}

	function switchTab(next: 'graph' | 'yaml') {
		if (next === tab) return;
		if (next === 'yaml') {
			if (doc && graphDirty) yaml = docToYaml(doc);
			tab = 'yaml';
			return;
		}
		// yaml -> graph: re-parse through the server (single validation source).
		api.flows
			.validate(yaml)
			.then((r) => {
				if (r.ok && r.doc) {
					doc = r.doc;
					// Place stages added in the YAML editor via the success-chain layout
					// rather than dropping them all on one point (which overlaps them).
					const auto = autoLayout(doc);
					for (const s of doc.stages) if (!(s.id in layout)) layout[s.id] = auto[s.id];
					graphDirty = false;
					validation = null;
					rev++;
					tab = 'graph';
				} else {
					tab = 'yaml';
					validation = {
						ok: false,
						title: t('page.flow.fixYaml'),
						errors: r.errors?.length ? r.errors : [t('page.flow.invalidFlow')]
					};
					validateOpen = true;
				}
			})
			.catch((e) => {
				validation = { ok: false, title: t('page.flow.validationFailed'), errors: [String(e)] };
				validateOpen = true;
			});
	}

	function onlink(source: string, handle: string, target: string) {
		if (!doc) return;
		if (source === START) {
			// Dragging the entry marker onto a stage makes it the entry (moves
			// it to document front); terminals can't be an entry.
			if (target !== DONE && target !== ABORT && target !== DEBUG && setEntry(doc, target))
				markChanged();
			return;
		}
		const s = doc.stages.find((x) => x.id === source);
		if (!s) return;
		if (handle === 'success') {
			// success never aborts or parks
			if (target !== s.id && target !== ABORT && target !== DEBUG) s.on_success = target;
		} else {
			const kind = handle as EdgeKind;
			if (target === DONE) return; // recoverable edges point at stages or abort, not done
			const existing = edgeOf(s, kind);
			if (target === ABORT) {
				// Dragging onto the abort terminal replaces the chain's first link
				// with the immediate stop (no max; nothing runs after abort).
				setEdge(s, kind, { to: ABORT });
			} else if (target === DEBUG) {
				// Dragging onto the debug leaf almost always means "and then a
				// human": it is appended after an existing finite chain. An empty
				// or unlimited chain becomes the bare leaf.
				const links = chainLinks(existing);
				const last = links[links.length - 1];
				if (last && last.to !== ABORT && last.to !== DEBUG && last.max !== -1)
					last.then = { to: DEBUG };
				else setEdge(s, kind, { to: DEBUG });
			} else {
				setEdge(s, kind, { to: target, max: existing?.max ?? 3, then: existing?.then });
			}
		}
		markChanged();
	}

	let layoutTimer: ReturnType<typeof setTimeout> | undefined;
	function onmove(id: string, x: number, y: number) {
		layout[id] = { x, y };
		// Layout-only autosave (DESIGN3 §2.2): same YAML = same hash = no new
		// version; deferred while real edits are pending (they save together).
		clearTimeout(layoutTimer);
		layoutTimer = setTimeout(async () => {
			if (!flow || dirty || flow.builtin) return;
			try {
				flow = await api.flows.save(projectId, flow.name, flow.yaml, { nodes: layout });
			} catch {
				/* next explicit save carries it */
			}
		}, 800);
	}

	// autoArrange re-runs the success-chain layout, discarding manual positions,
	// and persists the fresh map (via onmove's layout-only autosave path).
	function autoArrange() {
		if (!doc) return;
		layout = autoLayout(doc);
		rev++;
		const first = doc.stages[0]?.id;
		if (first) onmove(first, layout[first].x, layout[first].y);
	}

	async function validate() {
		validating = true;
		try {
			const r = await api.flows.validate(currentYAML());
			validation = r.ok
				? {
						ok: true,
						title: r.warnings?.length ? t('page.flow.validWarn') : t('page.flow.valid'),
						stages: r.stages,
						hash: r.hash,
						warnings: r.warnings
					}
				: { ok: false, title: t('page.flow.invalid'), errors: r.errors ?? [] };
			if (r.ok)
				toast(
					r.warnings?.length
						? t('page.flow.validToastWarn', {
								count: r.stages ?? stageCount,
								warnings: r.warnings.length
							})
						: t('page.flow.validToast', { count: r.stages ?? stageCount }),
					{ tone: r.warnings?.length ? 'warn' : 'ok' }
				);
		} catch (e) {
			validation = {
				ok: false,
				title: t('page.flow.validationFailed'),
				errors: [e instanceof Error ? e.message : String(e)]
			};
		} finally {
			validating = false;
		}
		validateOpen = true;
	}

	async function save() {
		if (!flow || busy || !dirty) return;
		busy = true;
		error = '';
		const wasBuiltin = flow.builtin;
		try {
			const y = currentYAML();
			flow = await api.flows.save(projectId, flow.name, y, { nodes: layout });
			yaml = flow.yaml;
			const r = await api.flows.validate(flow.yaml);
			if (r.ok && r.doc) {
				doc = r.doc;
				rev++;
			}
			graphDirty = false;
			validation = r.warnings?.length
				? {
						ok: true,
						title: t('page.flow.savedWarn'),
						stages: r.stages,
						hash: flow.hash,
						warnings: r.warnings
					}
				: null;
			notice = null;
			toast(
				wasBuiltin
					? t('page.flow.savedCopy', { hash: flow.hash.slice(0, 8) })
					: t('page.flow.saved', { hash: flow.hash.slice(0, 8) })
			);
			versions = await api.flows.versions(projectId, flow.name);
		} catch (e) {
			validation = {
				ok: false,
				title: t('page.flow.saveRefused'),
				errors: [e instanceof Error ? e.message : String(e)]
			};
			validateOpen = true;
		} finally {
			busy = false;
		}
	}

	// Clone rewrites the immutable top-level name (docToYaml/templates always
	// emit `name:` at column 0) and creates a fresh flow from the current editor
	// content plus node positions. Works on built-ins too — fork a default under
	// your own name without shadowing it.
	async function clone(name: string) {
		if (!flow) return;
		const y = currentYAML().replace(/^name:[^\n]*$/m, `name: ${name}`);
		await api.flows.create(projectId, name, y, { nodes: layout });
		cloning = false;
		toast(t('page.flow.cloned', { name }));
		leaving = true;
		await goto(to.flow(projectId, name));
	}

	async function loadVersion(v: FlowVersion) {
		if (
			dirty &&
			!(await confirmDialog({
				title: t('page.flow.replaceTitle'),
				body: th('page.flow.replaceBody', { hash: v.hash.slice(0, 8) }),
				action: t('page.flow.loadVersion'),
				tone: 'warn'
			}))
		)
			return;
		try {
			const full = await api.flows.version(projectId, v.hash);
			yaml = full.yaml; // into the YAML tab; Save makes it current again
			tab = 'yaml';
			graphDirty = false;
			showVersions = false;
			notice = {
				tone: 'info',
				text: t('page.flow.loaded', { hash: v.hash.slice(0, 8) })
			};
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function remove() {
		if (!flow) return;
		const reset = flow.shadowsBuiltin;
		const ok = await confirmDialog(
			reset
				? {
						title: t('page.flow.resetTitle', { name: flow.name }),
						body: th('page.flow.resetBody', { name: flow.name }),
						action: t('page.flow.resetAction')
					}
				: {
						title: t('page.flow.deleteTitle', { name: flow.name }),
						body: th('page.flow.deleteBody', { name: flow.name }),
						action: t('page.flow.deleteAction')
					}
		);
		if (!ok) return;
		try {
			await api.flows.remove(projectId, flow.name);
			toast(
				reset
					? t('page.flow.resetDone', { name: flow.name })
					: t('page.flow.deleted', { name: flow.name })
			);
			leaving = true;
			await goto(to.workshop(projectId, 'flows'));
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	// Leaving with unsaved changes asks first (DESIGN 4.9).
	let leaving = false;
	beforeNavigate((nav) => {
		if (!dirty || leaving) return;
		if (nav.type === 'leave') {
			nav.cancel(); // the browser shows its own "leave site?" prompt
			return;
		}
		nav.cancel();
		const dest = nav.to?.url;
		void confirmDialog({
			title: t('page.flow.leaveTitle'),
			body: th('page.flow.leaveBody', { name: flow?.name ?? '' }),
			action: t('page.flow.leaveAction')
		}).then((ok) => {
			if (ok && dest) {
				leaving = true;
				void goto(dest);
			}
		});
	});

	function keydown(e: KeyboardEvent) {
		const mod = e.metaKey || e.ctrlKey;
		if (mod && e.key.toLowerCase() === 's') {
			e.preventDefault();
			void save();
			return;
		}
		if (e.key !== 'Delete' && e.key !== 'Backspace') return;
		const el = e.target as HTMLElement | null;
		if (el?.closest('input, textarea, select, [contenteditable]')) return;
		if (tab === 'graph' && selection) {
			e.preventDefault();
			inspector?.deleteSelection();
		}
	}

	const stageCount = $derived(doc?.stages.length ?? 0);
</script>

<svelte:window onkeydown={keydown} />

<PageHeader
	crumbs={[
		{ label: guild.name, href: to.desk() },
		{ label: workshopName, href: to.workshop(projectId) },
		{ label: t('page.workshops.flows'), href: to.workshop(projectId, 'flows') },
		{ label: page.params.name ?? '', mono: true }
	]}
>
	{#snippet children()}
		<div class="trow">
			<h1 class="mono fname">{flow?.name ?? page.params.name}</h1>
			{#if flow}<span class="tag hash" title={t('page.flow.hashTitle')}
					>@{flow.hash.slice(0, 8)}</span
				>{/if}
			{#if flow?.builtin}<span class="pill t-mut">{t('page.flow.builtin')}</span>{/if}
			{#if flow?.shadowsBuiltin}<span class="pill t-mut">{t('page.flow.fromTemplate')}</span>{/if}
			{#if flow && fleet.projectById.get(projectId)?.defaultFlow === flow.name}<span
					class="pill t-brand">{t('page.flow.default')}</span
				>{/if}
		</div>
		<div class="meta">
			{#if doc?.description}<span class="ell desc">{doc.description}</span>{/if}
			{#if doc}<span>{t('page.flow.stages', { count: stageCount })}</span>{/if}
			{#if flow}<span title={fmtAbs(flow.updatedAt)}
					>{t('page.flow.savedAgo', { ago: ago(flow.updatedAt) })}</span
				>{/if}
		</div>
	{/snippet}
	{#snippet actions()}
		{#if flow}
			<div class="vc">
				<Segmented
					options={[
						{ value: 'graph', label: t('page.flow.graph') },
						{ value: 'yaml', label: 'YAML' }
					]}
					value={tab}
					onchange={(v) => switchTab(v)}
				/>
			</div>
			<GlassGroup>
				{#snippet label()}
					{#if dirty}
						<span class="unsaved">{t('page.flow.unsaved')}</span>
					{:else}
						<span>{t('page.flow.formula')}</span>
					{/if}
				{/snippet}
				<button
					bind:this={validateBtn}
					class="btn-sm"
					class:warnb={validation && !validation.ok}
					aria-busy={validating}
					onclick={validate}
					>{t('page.flow.validate')}{#if validation?.warnings?.length}<span
							class="pill sm mono t-amb">{validation.warnings.length}</span
						>{/if}</button
				>
				<button
					class="btn-sm"
					class:btn-sel={showVersions}
					onclick={() => (showVersions = !showVersions)}
					>{t('page.flow.versions')} <span class="mono fnt">{versions.length}</span></button
				>
				<button class="btn-sm" onclick={() => (cloning = true)} disabled={busy}
					>{t('page.flow.clone')}</button
				>
				{#if flow.shadowsBuiltin}
					<button class="btn-sm btn-danger" onclick={remove}>{t('page.flow.resetAction')}…</button>
				{:else if !flow.builtin}
					<button class="btn-sm btn-danger" onclick={remove}>{t('page.ticket.delete')}</button>
				{/if}
				<button
					class="btn-sm btn-primary"
					onclick={save}
					disabled={busy || !dirty}
					aria-busy={busy}
					title={flow.builtin ? t('page.flow.saveCopyTitle') : '⌘S'}
					>{busy
						? t('page.flow.saving')
						: flow.builtin
							? t('page.flow.saveAsCopy')
							: t('common.save')}</button
				>
			</GlassGroup>
		{/if}
	{/snippet}
</PageHeader>

<Popover
	anchor={validateBtn}
	bind:open={validateOpen}
	placement="below-end"
	width={380}
	title={t('page.flow.validation')}
>
	{#if validation}
		<div class="vp">
			<div class="vh">
				<CheckChip
					tone={validation.ok ? (validation.warnings?.length ? 'warn' : 'ok') : 'bad'}
					label={validation.title}
				/>
			</div>
			{#if validation.stages !== undefined || validation.hash}
				<div class="vm">
					{#if validation.stages !== undefined}<span
							>{t('page.flow.stagesN', { count: validation.stages })}</span
						>{/if}
					{#if validation.hash}<span class="mono">@{validation.hash.slice(0, 12)}</span>{/if}
				</div>
			{/if}
			{#if validation.errors?.length}
				<ul class="vl err">
					{#each validation.errors as e, i (i)}<li class="mono">{e}</li>{/each}
				</ul>
			{/if}
			{#if validation.warnings?.length}
				<div class="vw">
					{t('page.flow.bindingWarnings')} <span class="fnt">— {t('page.flow.advisory')}</span>
				</div>
				<ul class="vl warn">
					{#each validation.warnings as w (w)}<li>{w}</li>{/each}
				</ul>
			{/if}
		</div>
	{:else}
		<div class="vp mut small">{t('page.flow.checking')}</div>
	{/if}
</Popover>

{#if flow?.builtin}
	<div class="strip">
		<Callout glyph={I.rx}>
			{@html th('page.flow.builtinCallout')}
		</Callout>
	</div>
{/if}
{#if notice}
	<div class="strip">
		<Callout
			hue={notice.tone === 'ok' ? 'grn' : 'az'}
			glyph={notice.tone === 'ok' ? I.ok : I.info}
			ondismiss={() => (notice = null)}>{notice.text}</Callout
		>
	</div>
{/if}
{#if error}
	<div class="strip">
		<ErrorCard cause={t('page.flow.wrong')} detail={error} onretry={flow ? undefined : load} />
	</div>
{/if}

<div class="fe">
	{#if !flow && !error}
		<div class="canvas"><Skeleton pane lines={6} /></div>
	{:else if flow}
		<div class="canvas" class:yaml={tab === 'yaml'}>
			{#if tab === 'graph' && doc}
				<FlowGraph
					{doc}
					{layout}
					{rev}
					onselect={(s) => (selection = s)}
					{onmove}
					{onlink}
					onarrange={autoArrange}
					agents={doc?.agents ?? []}
					{actions}
				/>
				<div class="hint fnt">
					{@html th('page.flow.hint')}
				</div>
			{:else}
				<textarea
					class="yaml-ed bare"
					bind:value={yaml}
					spellcheck="false"
					aria-label={t('page.flow.yamlLabel')}></textarea>
			{/if}
		</div>

		{#if showVersions}
			<aside class="side lg" in:slide={{ from: 'right', distance: 24 }}>
				<div class="sh">
					<span class="serif st">{t('page.flow.versions')}</span>
					<span class="spacer"></span>
					<button
						class="btn-icon"
						aria-label={t('page.flow.closeVersions')}
						onclick={() => (showVersions = false)}>{I.close}</button
					>
				</div>
				<p class="sp">
					{t('page.flow.versionsHelp')}
				</p>
				<div class="vers">
					{#each versions as v (v.hash)}
						<div class="vr" class:cur={v.hash === flow.hash}>
							<span class="mono">@{v.hash.slice(0, 10)}</span>
							{#if v.hash === flow.hash}<span class="pill sm t-grn">{t('page.flow.current')}</span
								>{/if}
							<span class="spacer"></span>
							<span class="fnt tiny" title={fmtAbs(v.createdAt)}>{ago(v.createdAt)}</span>
							{#if v.hash !== flow.hash}
								<button class="btn-sm" onclick={() => loadVersion(v)}
									>{t('page.flow.loadIntoEditor')}</button
								>
							{/if}
						</div>
					{:else}
						<div class="fnt small">
							{flow.builtin ? t('page.flow.noVersionsBuiltin') : t('page.flow.noVersions')}
						</div>
					{/each}
				</div>
			</aside>
		{:else if tab === 'graph' && doc}
			<aside class="side lg" in:slide={{ from: 'right', distance: 24 }}>
				<FlowInspector
					bind:this={inspector}
					{doc}
					{selection}
					agents={doc?.agents ?? []}
					{actions}
					{signals}
					onchange={markChanged}
					onselect={(s) => (selection = s)}
				/>
			</aside>
		{/if}
	{/if}
</div>

{#if cloning && flow}
	<NameFlowDialog
		title={t('page.flow.cloneTitle', { name: flow.name })}
		initial="{flow.name}-copy"
		action={t('page.flow.cloneAction')}
		taken={[flow.name]}
		onsubmit={clone}
		onclose={() => (cloning = false)}
	>
		<p>
			{dirty ? t('page.flow.cloneHelpDirty') : t('page.flow.cloneHelp')}
		</p>
	</NameFlowDialog>
{/if}

<style>
	.trow {
		display: flex;
		align-items: center;
		gap: 10px;
		flex-wrap: wrap;
	}
	.fname {
		font-family: var(--mono);
		font-size: var(--fs-mark);
		font-weight: 600;
		letter-spacing: var(--tr-heading);
	}
	.hash {
		font-size: var(--fs-help);
		color: var(--mut);
	}
	.meta {
		display: flex;
		gap: 4px 14px;
		flex-wrap: wrap;
		font-size: var(--fs-label);
		color: var(--mut);
		min-width: 0;
	}
	.desc {
		max-width: 60ch;
	}
	.warnb {
		border-color: var(--rosLn);
		color: var(--ros);
	}
	.vp {
		padding: 2px 8px 8px;
		display: flex;
		flex-direction: column;
		gap: 8px;
		font-size: var(--fs-ui);
	}
	.vm {
		display: flex;
		gap: 12px;
		color: var(--mut);
		font-size: var(--fs-label);
	}
	.vl {
		margin: 0;
		padding-left: 16px;
		display: flex;
		flex-direction: column;
		gap: 4px;
		white-space: normal;
		max-height: 300px;
		overflow: auto;
	}
	.vl.err li {
		font-size: var(--fs-meta);
		color: var(--ros);
		white-space: pre-wrap;
	}
	.vl.warn li {
		font-size: var(--fs-label);
		color: var(--amb);
	}
	.vw {
		font-size: var(--fs-label);
		font-weight: 600;
	}
	.strip {
		margin: 0 16px 8px;
	}
	.fe {
		flex: 1;
		min-height: 0;
		display: flex;
		gap: 8px;
		padding: 0 16px 12px;
	}
	.canvas {
		position: relative;
		flex: 1;
		min-width: 0;
		border-radius: var(--r-pane);
		border: 1px solid var(--cLn);
		overflow: hidden;
	}
	.canvas.yaml {
		background: var(--cBg);
		backdrop-filter: var(--cBf);
		-webkit-backdrop-filter: var(--cBf);
	}
	.vc {
		display: flex;
		align-items: center;
	}
	.hint {
		position: absolute;
		right: 14px;
		top: 10px;
		font-size: var(--fs-meta);
		pointer-events: none;
	}
	/* .bare strips the field chrome (the canvas draws it). */
	.yaml-ed {
		width: 100%;
		height: 100%;
		min-height: 0;
		resize: none;
		border-radius: 0;
		font-family: var(--mono);
		font-size: var(--fs-ui);
		line-height: 1.65;
		padding: 14px 18px;
		tab-size: 2;
	}
	.side {
		flex: none;
		width: var(--aside-w);
		border-radius: var(--r-pane);
		overflow: auto;
	}
	.sh {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 12px 14px 8px;
	}
	.st {
		font-size: var(--fs-section);
	}
	.sp {
		margin: 0;
		padding: 0 14px 10px;
		font-size: var(--fs-help);
		color: var(--mut);
	}
	.vers {
		display: flex;
		flex-direction: column;
		padding: 0 8px 10px;
	}
	.vr {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 7px 8px;
		border-top: 1px solid var(--ln);
		font-size: var(--fs-label);
	}
	.vr.cur {
		background: var(--p2);
		border-radius: var(--r-row);
	}
	@media (max-width: 1000px) {
		.fe {
			flex-direction: column;
		}
		.canvas {
			min-height: 420px;
		}
		.side {
			width: 100%;
		}
	}
</style>
