// A run's sandbox size: the CPUs and memory it may use. A project sets its
// own, an admin sets the default and the ceiling (System → Limits). null is
// "not set": the default for a project's field, no default or no ceiling for
// an admin's.

export interface SandboxSize {
	/** CPUs (fractions allowed: 0.5 is half a core); null = not set. */
	cpus: number | null;
	/** Memory in GiB (fractions allowed); null = not set. */
	memoryGiB: number | null;
}

/** The instance's sandbox policy: what a project that sets nothing gets, the
 *  most any project may ask for, and how long a sandbox nobody uses stays up
 *  when its project does not say. */
export interface SandboxPolicy {
	default: SandboxSize;
	ceiling: SandboxSize;
	/** Minutes; 0 is never. */
	idleMinutes: number;
}

/** A project's idle setting that keeps its sandboxes up whatever the default. */
export const IDLE_NEVER = -1;

/** How long a project's sandbox may sit unused before it is stopped, in
 *  minutes (0: never): its own setting (0 the instance's, IDLE_NEVER never),
 *  else the instance's default (the engine's SandboxPolicy.IdleAfter). */
export function idleAfter(own: number, policyMinutes: number): number {
	const m = own === 0 ? policyMinutes : own;
	return m > 0 ? m : 0;
}

/** What one recent run's sandbox reached while the engine watched it. */
export interface SandboxRunUsage {
	runId: string;
	/** Peak working-set memory, bytes. */
	peakMem: number;
	/** The memory limit it ran under, bytes (the node's when uncapped). */
	memLimit: number;
	/** Processes the kernel killed at that limit. */
	oomKills: number;
	at: string;
}

export const unsetSize = (): SandboxSize => ({ cpus: null, memoryGiB: null });

type SizeKey = 'cpus' | 'memoryGiB';

/** A set size field's bounds, in the form's units (the engine's
 *  envport.MinMilliCPU … MaxMemoryMiB). */
export const SIZE_BOUNDS: Record<SizeKey, { min: number; max: number }> = {
	cpus: { min: 0.1, max: 1000 },
	memoryGiB: { min: 0.5, max: 16384 }
};

/** A size field is unset (null) or a number within its bounds. */
export function sizeFieldValid(v: number | null, key: SizeKey): boolean {
	const b = SIZE_BOUNDS[key];
	return v === null || (Number.isFinite(v) && v >= b.min && v <= b.max);
}

/** Which of size's fields is out of its bounds, by name; empty when none. */
export function outOfBounds(size: SandboxSize): SizeKey[] {
	return (['cpus', 'memoryGiB'] as const).filter((k) => !sizeFieldValid(size[k], k));
}

/** size with only the fields that differ from prev; the others unset. What
 *  a save checks: a field it keeps is never refused (the engine's
 *  SandboxPolicy.CheckChange). */
export function changedFields(prev: SandboxSize, size: SandboxSize): SandboxSize {
	return {
		cpus: size.cpus === prev.cpus ? null : size.cpus,
		memoryGiB: size.memoryGiB === prev.memoryGiB ? null : size.memoryGiB
	};
}

/** Which of size's fields is above the ceiling, by name; empty when none. */
export function aboveCeiling(size: SandboxSize, ceiling: SandboxSize): ('cpus' | 'memoryGiB')[] {
	const out: ('cpus' | 'memoryGiB')[] = [];
	for (const k of ['cpus', 'memoryGiB'] as const) {
		const v = size[k];
		const c = ceiling[k];
		if (v !== null && c !== null && v > c) out.push(k);
	}
	return out;
}

/** The size a project's sandbox gets: each field its own, else the default;
 *  never above the ceiling. A field neither sets stays unset: the ceiling
 *  caps it (sizeCap), it is not its size (the engine's
 *  SandboxPolicy.Effective). */
export function effectiveSize(own: SandboxSize, policy: SandboxPolicy): SandboxSize {
	const pick = (o: number | null, d: number | null, c: number | null) => {
		const v = o ?? d;
		if (c !== null && v !== null && v > c) return c;
		return v;
	};
	return {
		cpus: pick(own.cpus, policy.default.cpus, policy.ceiling.cpus),
		memoryGiB: pick(own.memoryGiB, policy.default.memoryGiB, policy.ceiling.memoryGiB)
	};
}

/** The most a sandbox of the effective size may use where that size sets
 *  nothing: the ceiling, a limit never reserved (SandboxPolicy.Cap). */
export function sizeCap(effective: SandboxSize, policy: SandboxPolicy): SandboxSize {
	return {
		cpus: effective.cpus === null ? policy.ceiling.cpus : null,
		memoryGiB: effective.memoryGiB === null ? policy.ceiling.memoryGiB : null
	};
}

/** The highest peak among the runs, and the most OOM kills any of them saw;
 *  null when there are none. */
export function usageSummary(
	runs: SandboxRunUsage[]
): { peakMem: number; oomKills: number; runs: number } | null {
	if (runs.length === 0) return null;
	return {
		peakMem: Math.max(...runs.map((r) => r.peakMem)),
		oomKills: runs.reduce((n, r) => n + r.oomKills, 0),
		runs: runs.length
	};
}

/** What a sandbox runtime lets a workspace run. */
export interface RuntimeCapabilities {
	/** A docker daemon (and compose) inside the sandbox. */
	nestedContainers: boolean;
	/** A Kubernetes cluster on that daemon (k3d, kind). */
	nestedKubernetes: boolean;
	/** User-namespaced pods inside the sandbox. */
	userNamespaces: boolean;
	/** Sandbox root is not root on its node. */
	contained: boolean;
}

/** One runtime an admin approved. */
export interface SandboxRuntime {
	name: string;
	capabilities: RuntimeCapabilities;
	/** Why its idle sandboxes cannot be stopped and kept, whatever the storage (a VM's disks). */
	noSleep?: string;
}

/** What a workshop may choose for its sandboxes' runtime. */
export interface RuntimeOffer {
	/** false: the provisioner offers no choice; the form shows none. */
	offered: boolean;
	default: string;
	approved: SandboxRuntime[];
	/** Why the provisioner's offer could not be read. */
	error?: string;
	/** Why idle sandboxes cannot be stopped and kept there (emptyDir storage). */
	noSleep?: string;
}

/** Why the workshop's idle sandboxes cannot be stopped and kept under its pick; undefined when they can. */
export const sleepRefusal = (offer: RuntimeOffer, pick: string): string | undefined =>
	offer.noSleep ?? offer.approved.find((r) => r.name === effectiveRuntime(offer, pick))?.noSleep;

/** The runtime a workshop's sandboxes run under: its pick, else the default. */
export const effectiveRuntime = (offer: RuntimeOffer, pick: string): string =>
	pick || offer.default;

/** Why the workshop cannot run under its pick, as a message key; null when it can. */
export function runtimeProblem(
	offer: RuntimeOffer,
	pick: string,
	docker: boolean
): 'notApproved' | 'noDocker' | null {
	const rt = offer.approved.find((r) => r.name === effectiveRuntime(offer, pick));
	if (!rt) return 'notApproved';
	if (docker && !rt.capabilities.nestedContainers) return 'noDocker';
	return null;
}
