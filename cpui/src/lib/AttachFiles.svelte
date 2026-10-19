<script lang="ts" module>
	import { api as apiMod } from '$lib/api';
	import type { UploadLimits as UL } from '$lib/domain';
	let limitsPromise: Promise<UL | null> | null = null;
	function limitsOnce(): Promise<UL | null> {
		if (!limitsPromise) {
			limitsPromise = apiMod.files.limits().catch(() => null);
		}
		return limitsPromise;
	}
</script>

<script lang="ts">
	// Attachments picker (DESIGN 4.15): a drop zone plus removable chips with
	// thumbnails. The parent owns the selected files (bindable) and uploads
	// them on submit via api.files.upload. Documents/images attached here reach
	// the agent.
	//
	// The server's upload policy is mirrored client-side (fetched once): a pick
	// that would be rejected — too big, too many, off the MIME allowlist — is
	// refused INLINE with the reason, instead of after a failed round trip.
	import { fmtBytes } from '$lib/format';
	import type { UploadLimits } from '$lib/domain';
	import FileChip from '$lib/ui/FileChip.svelte';
	import { I } from '$lib/ui/icons';
	import { t } from '$lib/i18n/index.svelte';

	let {
		files = $bindable([]),
		label,
		compact = false
	}: { files?: File[]; label?: string; compact?: boolean } = $props();

	let inputEl = $state<HTMLInputElement | null>(null);
	let refused = $state<{ name: string; size: number; why: string }[]>([]);
	let over = $state(false);

	// One fetch per session, shared by every picker instance.
	let limits = $state<UploadLimits | null>(null);
	$effect(() => {
		limitsOnce().then((l) => (limits = l));
	});

	const kinds = $derived.by(() => {
		if (!limits) return '';
		const k = new Set<string>();
		for (const m of limits.mimeAllow) {
			if (m.startsWith('image/')) k.add(t('attach.kind.images'));
			else if (m.includes('pdf')) k.add('pdf');
			else if (m.includes('markdown')) k.add('md');
			else if (m.startsWith('text/')) k.add(t('attach.kind.text'));
		}
		return [...k].join(', ');
	});

	const urls = new Map<File, string>();
	function preview(f: File): string {
		let u = urls.get(f);
		if (!u) {
			u = URL.createObjectURL(f);
			urls.set(f, u);
		}
		return u;
	}

	function allowed(l: UploadLimits, mime: string): boolean {
		const m = mime.toLowerCase().split(';')[0].trim();
		if (!m) return true; // unknown here — let the server sniff it
		return l.mimeAllow.some((a) =>
			a.endsWith('/*') ? m.startsWith(a.slice(0, -1)) : m === a.toLowerCase()
		);
	}

	function take(list: FileList | File[] | null | undefined) {
		refused = [];
		if (!list) return;
		const add: File[] = [];
		const no: typeof refused = [];
		for (const f of Array.from(list)) {
			if (limits && f.size > limits.maxBytes) {
				no.push({
					name: f.name,
					size: f.size,
					why: t('attach.overLimit', { size: fmtBytes(limits.maxBytes) })
				});
			} else if (limits && !allowed(limits, f.type)) {
				no.push({
					name: f.name,
					size: f.size,
					why: t('attach.typeNotAllowed', { type: f.type || t('attach.unknownType') })
				});
			} else if (limits && files.length + add.length >= limits.maxFiles) {
				no.push({
					name: f.name,
					size: f.size,
					why: t('attach.tooMany', { count: limits.maxFiles })
				});
			} else {
				add.push(f);
			}
		}
		files = [...files, ...add];
		refused = no;
	}

	function onPick(e: Event) {
		take((e.target as HTMLInputElement).files);
		if (inputEl) inputEl.value = ''; // allow re-picking the same file
	}
	function onDrop(e: DragEvent) {
		e.preventDefault();
		over = false;
		take(e.dataTransfer?.files);
	}
	const remove = (i: number) => (files = files.filter((_, j) => j !== i));
</script>

<div class="attach">
	<button
		type="button"
		class="drop"
		class:over
		class:compact
		onclick={() => inputEl?.click()}
		ondragover={(e) => {
			e.preventDefault();
			over = true;
		}}
		ondragleave={() => (over = false)}
		ondrop={onDrop}
	>
		<span class="clip">{I.attach}</span>
		<span>{label ?? t('attach.label')}</span>
		{#if limits && !compact}
			<span class="fnt"
				>{t('attach.max', { size: fmtBytes(limits.maxBytes) })}{kinds ? ` · ${kinds}` : ''}</span
			>
		{/if}
	</button>
	<input bind:this={inputEl} type="file" multiple hidden onchange={onPick} />
	{#if files.length || refused.length}
		<div class="chips">
			{#each files as f, i (i)}
				<FileChip
					name={f.name}
					size={f.size}
					thumb={f.type.startsWith('image/') ? preview(f) : undefined}
					onremove={() => remove(i)}
				/>
			{/each}
			{#each refused as r (r.name)}
				<FileChip name={r.name} state="refused" reason={r.why} />
			{/each}
		</div>
	{/if}
</div>

<style>
	.attach {
		display: flex;
		flex-direction: column;
		gap: 8px;
		min-width: 0;
	}
	/* The drop zone (DESIGN 4.15): dashed, lights up under a dragged file. */
	.drop {
		width: 100%;
		justify-content: center;
		flex-wrap: wrap;
		white-space: normal;
		padding: 12px;
		background: transparent;
		box-shadow: none;
		border: 1px dashed var(--ln2);
		color: var(--mut);
		text-align: center;
	}
	.drop.compact {
		justify-content: flex-start;
		padding: 6px 10px;
	}
	.drop:hover,
	.drop.over {
		border-color: var(--brand);
		background: var(--brandBg);
		color: var(--tx);
	}
	.clip {
		font-size: var(--fs-body);
	}
	.chips {
		gap: 6px;
	}
</style>
