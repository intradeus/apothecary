<script lang="ts">
	// A stage's routes: every way out of it, each link of a failure route on
	// a row of its own. The canvas picks one out by selecting its edge.
	import { untrack } from 'svelte';
	import Select from '$lib/ui/Select.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { EDGE_HUE } from '$lib/design/hues';
	import { I } from '$lib/design/icons';
	import Caption from '$lib/flow/inspector/Caption.svelte';
	import {
		ABORT,
		DEBUG,
		DONE,
		END,
		FAIL,
		chainLinks,
		edgeKindsFor,
		edgeOf,
		feedSourceFor,
		givesVerdict,
		isReservedTarget,
		routableFrom,
		setEdge,
		type DocAgent,
		type DocEdge,
		type DocSequence,
		type DocStage,
		type EdgeKind,
		type FlowDoc
	} from '$lib/domain/flowdoc';

	let {
		doc,
		stage: s,
		scopeSeq,
		focusRoute,
		agents,
		onchange
	}: {
		doc: FlowDoc;
		stage: DocStage;
		/** The sequence the stage is written in, if any. */
		scopeSeq?: DocSequence;
		/** The route the selected edge is: scrolled to and marked. */
		focusRoute?: string;
		agents: DocAgent[];
		onchange: () => void;
	} = $props();

	const scopeStages = $derived(scopeSeq ? scopeSeq.stages : doc.stages);
	// The stages a stage may route to: its sequence's, or its chain's.
	const routable = $derived(scopeSeq ? scopeSeq.stages : routableFrom(doc, s));

	let routesEl = $state<HTMLElement | null>(null);
	$effect(() => {
		const kind = focusRoute;
		if (!kind || !routesEl) return;
		untrack(() =>
			routesEl
				?.querySelector(`[data-route="${kind}"]`)
				?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
		);
	});

	// A new route starts somewhere legal: a stage of the same chain (a route
	// may not leave it). A failure or timeout retries the stage itself; a
	// gate's rejection goes back to whatever leads into the gate.
	function addEdge(kind: EdgeKind) {
		const mine = routable;
		const feeder = mine.find((x) => x.on_success === s.id && x.id !== s.id);
		const to = s.type === 'human_gate' ? (feeder ?? mine.find((x) => x.id !== s.id))?.id : s.id;
		setEdge(s, kind, to ? { to, max: 3 } : { to: DEBUG });
		onchange();
	}

	const targetsHuman = (id: string) =>
		['human_gate', 'manual'].includes(scopeStages.find((x) => x.id === id)?.type ?? '');
	// Unlimited (-1) needs a human on the link: a gate or manual target (each
	// firing parks on a person) or a gate source (the link only fires on reject).
	const unlimitedOK = (to: string) => s.type === 'human_gate' || targetsHuman(to);

	// retarget switches one chain link's target, normalizing the link's other
	// fields: links to a reserved target (end, done, abort, debug) carry no
	// max/then; stage links need a max, and -1 survives only while a gate
	// remains on the link.
	function retarget(link: DocEdge, to: string) {
		link.to = to;
		if (isReservedTarget(to)) {
			link.max = undefined;
			link.then = undefined;
		} else if (link.max == null || (link.max === -1 && !unlimitedOK(to))) {
			link.max = 3;
		}
		onchange();
	}

	// appendLink adds a fallback ("then") after the chain's last link.
	function appendLink(e: DocEdge) {
		let last = e;
		while (last.then) last = last.then;
		if (isReservedTarget(last.to) || last.max === -1) return; // nothing follows a reserved target or an unlimited link
		// The usual fallback once retries are spent: a human with the workspace.
		last.then = { to: DEBUG };
		onchange();
	}

	// removeLink takes one link out of a route, the links after it moving up
	// (the route goes with its only link).
	function removeLink(kind: EdgeKind, depth: number) {
		const links = chainLinks(edgeOf(s, kind));
		if (depth === 0) setEdge(s, kind, links[0]?.then);
		else links[depth - 1].then = links[depth].then;
		onchange();
	}
	// A link's limit: how many times it may send the run to its target
	// (a whole number from 1), or none — a human decides each time.
	function setMax(link: DocEdge, text: string) {
		const n = Number(text);
		if (text.trim() === '' || !Number.isInteger(n) || n < 1) return;
		link.max = n;
		onchange();
	}
	function setUnlimited(link: DocEdge, on: boolean) {
		link.max = on ? -1 : 3;
		if (on) link.then = undefined;
		onchange();
	}
</script>

<!-- Every way out of the stage, one row each in the same shape: its
     kind, where it goes — and for a failure route, how many times,
     then what. -->
<div class="routes" bind:this={routesEl}>
	<Caption text={t('flow.insp.routes')} />
	<div class="route" class:hl={focusRoute === 'success'} data-route="success">
		<div class="link">
			<span
				class="rk mono tint-{EDGE_HUE.success}"
				title={givesVerdict(s, agents)
					? t('flow.insp.successCarries')
					: t('flow.insp.successTitle')}>success</span
			>
			<span class="to">
				<Select
					id="st-succ"
					label={t('flow.insp.successTitle')}
					value={s.on_success ?? ''}
					options={[
						// In a sequence, saying nothing goes on to the next stage —
						// or, from the last, out by the sequence stage's success.
						...(scopeSeq
							? [
									{
										value: '',
										label:
											scopeSeq.stages.at(-1)?.id === s.id
												? t('flow.insp.leaveOption')
												: t('flow.insp.nextOption', {
														next: scopeSeq.stages[scopeSeq.stages.indexOf(s) + 1]?.id ?? ''
													})
									}
								]
							: []),
						...routable.filter((x) => x.id !== s.id).map((x) => ({ value: x.id, label: x.id })),
						{ value: END, label: t('flow.insp.endOption') },
						{ value: DONE, label: t('flow.insp.doneOption') },
						{ value: ABORT, label: t('flow.insp.abortOption') }
					]}
					onchange={(v) => {
						s.on_success = v || undefined;
						onchange();
					}}
				/>
			</span>
		</div>
	</div>
	{#each edgeKindsFor(s.type) as kind (kind)}
		{@const e = edgeOf(s, kind)}
		{#if e}
			{@const links = chainLinks(e)}
			{@const last = links[links.length - 1]}
			<div class="route" class:hl={focusRoute === kind} data-route={kind}>
				{#each links as link, depth (depth)}
					<div class="link">
						<span
							class="rk mono tint-{EDGE_HUE[kind]}"
							class:then={depth > 0}
							title={t('flow.insp.routeCarries', {
								kind,
								sentinel: feedSourceFor(kind)
							})}>{depth > 0 ? 'then' : kind}</span
						>
						<span class="to">
							<Select
								id="rt-{kind}-{depth}"
								label={depth > 0 ? `${kind} · then #${depth}` : kind}
								value={link.to}
								options={[
									...routable.map((x) => ({ value: x.id, label: x.id })),
									...(scopeSeq ? [{ value: FAIL, label: t('flow.insp.failOption') }] : []),
									{ value: END, label: t('flow.insp.endOption') },
									{ value: DONE, label: t('flow.insp.doneOption') },
									{ value: ABORT, label: t('flow.insp.abortOption') },
									{ value: DEBUG, label: t('flow.insp.debugOption') }
								]}
								onchange={(v) => retarget(link, v)}
							/>
						</span>
						{#if !isReservedTarget(link.to)}
							{#if link.max === -1}
								<button
									class="btn-sm btn-sel unl"
									title={t('flow.insp.limitAgain')}
									onclick={() => setUnlimited(link, false)}>∞</button
								>
							{:else}
								<span class="x" aria-hidden="true">×</span>
								<input
									id="rt-{kind}-{depth}-max"
									class="mono max"
									type="number"
									min="1"
									aria-label={t('flow.insp.max')}
									title={t('flow.insp.maxTitle')}
									value={link.max ?? 1}
									oninput={(ev) => setMax(link, ev.currentTarget.value)}
								/>
								{#if unlimitedOK(link.to)}
									<button
										class="btn-sm unl"
										aria-label={t('flow.insp.noLimit')}
										title={targetsHuman(link.to)
											? t('flow.insp.noLimitTitleGate')
											: t('flow.insp.noLimitTitle')}
										onclick={() => setUnlimited(link, true)}>∞</button
									>
								{/if}
							{/if}
						{/if}
						<button
							class="btn-icon"
							title={t('flow.insp.removeLink')}
							aria-label={t('flow.insp.removeLink')}
							onclick={() => removeLink(kind, depth)}>{I.close}</button
						>
					</div>
				{/each}
				{#if !isReservedTarget(last.to) && last.max !== -1}
					<button class="btn-sm btn-ghost add-then" onclick={() => appendLink(e)}
						>{I.add} {t('flow.insp.thenLink')}</button
					>
				{/if}
			</div>
		{/if}
	{/each}
	<div class="chips">
		{#each edgeKindsFor(s.type).filter((k) => !edgeOf(s, k)) as kind (kind)}
			<button class="btn-sm hue-{EDGE_HUE[kind]}" onclick={() => addEdge(kind)}
				>{I.add} {kind}</button
			>
		{/each}
	</div>
	<div class="help">
		{t(scopeSeq ? 'flow.insp.endsHelpSequence' : 'flow.insp.endsHelp')}
	</div>
	{#if edgeKindsFor(s.type).length}
		<div class="help">{t('flow.insp.routesHelp')}</div>
	{/if}
	{#if edgeKindsFor(s.type).includes('on_timeout')}
		<div class="help">{t('flow.insp.timeoutsHelp')}</div>
	{/if}
</div>

<style>
	/* A stage's routes: one row per link, each in its route's hue. */
	.routes {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.route {
		display: flex;
		flex-direction: column;
		gap: 4px;
		padding: 6px 8px;
		border-radius: var(--r-row);
		border: 1px solid var(--line);
		transition:
			border-color var(--dur-fade) var(--ease-out),
			box-shadow var(--dur-fade) var(--ease-out);
	}
	/* The route an edge picked on the canvas. */
	.route.hl {
		border-color: var(--brand-line);
		box-shadow: 0 0 0 3px var(--brand-bg);
	}
	.link {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
	}
	.rk {
		flex: none;
		width: 84px;
		font-size: var(--fs-id);
		font-weight: 600;
	}
	.rk.then {
		padding-left: 12px;
		opacity: 0.75;
	}
	.to {
		flex: 1;
		min-width: 0;
	}
	.x {
		color: var(--text-muted);
	}
	.max {
		width: 56px;
	}
	.unl {
		min-width: 28px;
		padding-inline: 6px;
	}
	.add-then {
		align-self: flex-start;
		margin-left: 84px;
	}
</style>
