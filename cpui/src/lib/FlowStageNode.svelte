<script lang="ts">
	import { Handle, Position, useUpdateNodeInternals, type NodeProps } from '@xyflow/svelte';
	import StageChip from '$lib/ui/StageChip.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { EDGE_HUE, STAGE_HUE, hueOf } from '$lib/ui/hues';
	import {
		isFeedSentinel,
		stageInputs,
		stageOutputs,
		type ActionContract,
		type AgentContract,
		type DocStage
	} from '$lib/flowdoc';

	let { id, data, selected }: NodeProps = $props();
	const stage = $derived(data.stage as DocStage);
	const entry = $derived(Boolean(data.entry));
	// Lifecycle marks from FlowGraph: a stage just added materialises; a
	// stage just deleted dematerialises before it goes.
	const fresh = $derived(Boolean(data.fresh));
	const leaving = $derived(Boolean(data.leaving));
	const agents = $derived((data.agents ?? []) as AgentContract[]);
	const actions = $derived((data.actions ?? []) as ActionContract[]);

	// Dataflow ports: one dot per artifact the stage reads (left) and writes
	// (right). Distinct from the colored control handles: these are NOT
	// rewireable — the wiring IS the artifact paths, so you change it by editing
	// an input or output, and the data edges follow.
	const inputs = $derived(stageInputs(stage, agents, actions));
	const outputs = $derived(stageOutputs(stage, agents, actions));

	// xyflow caches handle geometry per node id, and a node's id is stable while
	// its dots come and go (bindings change as the document is edited).
	// Telling it to re-measure is what keeps the data edges attached.
	const updateNodeInternals = useUpdateNodeInternals();
	const dotSignature = $derived(
		[
			...inputs.map((p) => `i:${p.name}:${p.value}`),
			...outputs.map((p) => `o:${p.name}:${p.value}`)
		].join('|')
	);
	$effect(() => {
		void dotSignature;
		updateNodeInternals(id);
	});

	// A control handle in its edge's hue (DESIGN 3.6), through Svelte Flow's
	// own handle colour variable.
	const hh = (kind: keyof typeof EDGE_HUE) =>
		`--xy-handle-background-color: var(--${EDGE_HUE[kind]})`;
	// `unset` marks a placeholder (nothing chosen yet), styled muted.
	const detail = $derived.by((): { text: string; unset: boolean } => {
		const set = (text: string) => ({ text, unset: false });
		const unset = (text: string) => ({ text, unset: true });
		if (stage.type === 'agent')
			return stage.agent ? set(stage.agent) : unset(t('flow.node.noAgent'));
		if (stage.type === 'command')
			return stage.run_ref
				? set(stage.run_ref)
				: stage.run
					? set(t('flow.node.script'))
					: unset(t('flow.node.noCommand'));
		if (stage.type === 'action')
			return stage.action ? set(stage.action) : unset(t('flow.node.noAction'));
		if (stage.type === 'wait')
			return stage.for
				? set(t('flow.node.waitFor', { signal: stage.for }))
				: unset(t('flow.node.waitFor', { signal: t('flow.node.noSignal') }));
		return set(stage.question || t('flow.node.asksHuman'));
	});
</script>

<div
	class="stage-node c h-{hueOf(STAGE_HUE, stage.type)}"
	class:selected
	class:materialize={fresh}
	class:leaving
>
	<div class="head">
		<!-- The un-id'd target: every control edge lands here unless the target
         bound the value it carries, in which case it lands on that dot. -->
		<Handle type="target" position={Position.Left} style="top: 20px" />
		<StageChip type={stage.type} />
		<span class="mono id">{stage.id}</span>
		{#if entry}<span class="pill sm flat sq t-brand entry" title={t('flow.node.entryTitle')}
				>{t('flow.node.entry')}</span
			>{/if}
		<Handle
			id="success"
			type="source"
			position={Position.Right}
			style="top: 20px; {hh('success')}"
		/>
	</div>
	<div class="meta" class:unset={detail.unset}>{detail.text}</div>

	{#if inputs.length || outputs.length}
		<div class="ports">
			{#each inputs as p (p.name)}
				<!-- A feed sentinel ($DECISION/$FAILURE) gets a dot too: the control
             edge that carries it lands there, so the retry path is visible. -->
				<div
					class="io in"
					class:unbound={p.unbound}
					class:sentinel={p.sentinel}
					class:fed={isFeedSentinel(p.value)}
					title={p.unbound ? t('flow.node.unbound') : p.value}
				>
					{#if !p.unbound && (!p.sentinel || isFeedSentinel(p.value))}
						<Handle
							id={`in:${p.name}`}
							type="target"
							position={Position.Left}
							isConnectable={false}
							class="io-dot"
						/>
					{:else}
						<span class="vdot" aria-hidden="true"></span>
					{/if}
					<span class="pname mono">{p.name}</span>
					{#if p.unbound}<span class="unset">{t('flow.node.unbound')}</span>
					{:else if p.sentinel}<span class="sent mono">{p.value}</span>
					{:else if p.value !== p.name}<span class="path mono">{p.value}</span>{/if}
				</div>
			{/each}

			{#each outputs as p (p.name)}
				<div class="io out" class:sentinel={p.sentinel} title={p.value}>
					{#if p.value && p.value !== p.name}<span class="path mono" class:sent={p.sentinel}
							>{p.value}</span
						>{/if}
					<span class="pname mono">{p.name}</span>
					{#if p.value}
						<Handle
							id={`out:${p.name}`}
							type="source"
							position={Position.Right}
							isConnectable={false}
							class="io-dot"
						/>
					{/if}
				</div>
			{/each}
		</div>
	{/if}

	{#if stage.type === 'agent' || stage.type === 'command' || stage.type === 'wait'}
		<Handle
			id="on_fail"
			type="source"
			position={Position.Bottom}
			style="left: 25%; {hh('on_fail')}"
		/>
		<Handle
			id="on_timeout"
			type="source"
			position={Position.Bottom}
			style="left: 50%; {hh('on_timeout')}"
		/>
	{/if}
	{#if stage.type === 'agent' || stage.type === 'human_gate'}
		<Handle
			id="on_reject"
			type="source"
			position={Position.Bottom}
			style="left: 75%; {hh('on_reject')}"
		/>
	{/if}
</div>

<style>
	/* Content glass (.c), radius 14, a top accent in the stage-type hue
	   (DESIGN 7.8). Selection and hover ride the spring: the ring and glow
	   grow in and retarget from wherever they are when the pointer moves on. */
	.stage-node {
		position: relative;
		border-top: var(--node-accent) solid var(--h);
		border-radius: var(--r-card);
		padding: 8px 12px 10px;
		min-width: 190px;
		max-width: 300px;
		font-size: var(--fs-label);
		color: var(--tx);
		transition:
			box-shadow var(--dur-spring) var(--spring),
			border-color var(--dur-spring) var(--spring);
	}
	.stage-node:hover {
		border-color: color-mix(in oklch, var(--h) 38%, transparent);
		border-top-color: var(--h);
		box-shadow:
			0 0 0 1px color-mix(in oklch, var(--h) 38%, transparent),
			0 0 var(--glow-md) color-mix(in oklch, var(--h) 14%, transparent),
			var(--cSh);
	}
	.stage-node.selected {
		border-color: var(--brandLn);
		border-top-color: var(--h);
		box-shadow:
			0 0 0 1.5px var(--brand),
			0 0 calc(var(--glow-md) * 1.5) var(--brandGlow),
			var(--cSh);
	}
	/* Deleted: it leaves the way a surface arrives, in reverse. */
	.leaving {
		pointer-events: none;
		animation: fsn-leave var(--dur-spring) var(--spring) both;
	}
	@keyframes fsn-leave {
		to {
			opacity: 0;
			transform: scale(var(--enter-scale));
			filter: blur(var(--enter-blur));
		}
	}
	.head {
		display: flex;
		gap: 6px;
		align-items: center;
	}
	.id {
		font-weight: 600;
		font-size: var(--fs-label);
	}
	.entry {
		margin-left: auto;
	}
	.meta {
		font-size: var(--fs-meta);
		color: var(--mut);
		margin-top: 4px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.meta.unset {
		color: var(--amb);
		font-style: italic;
	}
	.ports {
		margin-top: 7px;
		padding-top: 5px;
		border-top: 1px solid var(--cLn);
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	/* One io row per port; the dot sits on the node's edge, and xyflow
	   measures handle geometry from the DOM. */
	.io {
		position: relative;
		display: flex;
		gap: 5px;
		align-items: baseline;
		font-size: var(--fs-id);
		line-height: 15px;
		white-space: nowrap;
		min-width: 0;
	}
	.io.out {
		justify-content: flex-end;
	}
	.pname {
		font-size: var(--fs-id);
		font-weight: 600;
	}
	.path,
	.unset,
	.sent {
		font-size: var(--fs-id);
		color: var(--fnt);
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}
	.sent {
		color: var(--vio);
	}
	.unset {
		font-style: italic;
		color: var(--amb);
	}
	/* An edge-carried input only fills on a retry: present, but not the
	   stage's main business. */
	.io.fed {
		opacity: 0.8;
	}
	/* Data dots read as plumbing, not as something you can grab: bound green,
	   unbound amber hollow, sentinel violet. Not connectable. Their colour and
	   size go through the handle variables FlowGraph maps (--xy-handle-*,
	   --hs), so nothing here fights Svelte Flow's own rules. */
	.stage-node :global(.io-dot) {
		--hs: calc(var(--handle) - 1px);
		--xy-handle-background-color: var(--grn);
		top: 50%;
		cursor: default;
	}
	.vdot {
		position: absolute;
		top: 50%;
		width: calc(var(--handle) - 1px);
		height: calc(var(--handle) - 1px);
		border-radius: 50%;
		background: var(--grn);
		border: 1px solid var(--body);
	}
	.io.in :global(.io-dot),
	.io.in .vdot {
		left: -13px;
		transform: translate(-50%, -50%);
	}
	.io.out :global(.io-dot) {
		right: -13px;
		left: auto;
		transform: translate(50%, -50%);
	}
	.io.unbound .vdot {
		background: transparent;
		border-color: var(--amb);
	}
	.io.sentinel .vdot {
		background: var(--vio);
	}
	.io.sentinel :global(.io-dot) {
		--xy-handle-background-color: var(--vio);
	}
</style>
