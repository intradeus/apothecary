// A workshop's services: its long-running processes (the app, a database),
// declared once on the workshop and run in each run's workspace — started by
// a flow's `service` stage or by a person from the run page.

/** One service as the workshop declares it. */
export interface ServiceConfig {
	/** A slug: what a flow's `service:` and `run.service.<name>` name. */
	name: string;
	description?: string;
	/** The script that runs it, in the primary checkout. One that keeps
	 *  running IS the service; one that exits 0 started something in the
	 *  background, and `stop` brings that down. */
	start: string;
	stop?: string;
	health?: ServiceHealth;
	/** Where it listens; the first is its address. */
	ports: ServicePort[];
}

/** When a service is up: at most one probe (none = up once started). */
export interface ServiceHealth {
	/** A URL probed in the workspace; any 2xx or 3xx is healthy. */
	http?: string;
	/** A port that accepts connections. */
	tcp?: number;
	/** A script that exits 0. */
	command?: string;
	/** Durations (Go syntax): how long it may take to come up (default 5m),
	 *  and how often it is probed once up (default 10s). */
	timeout?: string;
	interval?: string;
}

export interface ServicePort {
	port: number;
	label?: string;
	/** What its link opens, "/" by default. */
	path?: string;
}

/** stopped with no startedAt = never started in this workspace. */
export type ServiceState =
	'stopped' | 'starting' | 'healthy' | 'unhealthy' | 'exited' | 'stopping' | 'unknown';

/** One service as it stands in a run's workspace right now. */
export interface ServiceStatus {
	name: string;
	description?: string;
	state: ServiceState;
	healthy: boolean;
	/** Who started it last. */
	by?: 'stage' | 'human';
	/** The stage that started it (by = stage), and its watcher's phase. */
	stageId?: string;
	phase?: string;
	startedAt?: string;
	healthyAt?: string;
	endedAt?: string;
	exitCode?: number;
	/** The probe in words ("GET http://localhost:3000/health", "tcp 5432");
	 *  empty = up once started. */
	probe?: string;
	lastProbeAt?: string;
	lastProbeOk: boolean;
	lastError?: string;
	ports: ServicePortStatus[];
}

export interface ServicePortStatus {
	port: number;
	label?: string;
	path?: string;
	/** Its address inside the workspace (http://localhost:3000/). */
	address: string;
	/** The preview link for a person, present while the service is healthy. */
	url?: string;
}

/** A run's services. workspace false = no environment, nothing can start;
 *  error = the workspace could not be read just now (states are unknown). */
export interface RunServices {
	workspace: boolean;
	error?: string;
	services: ServiceStatus[];
}
