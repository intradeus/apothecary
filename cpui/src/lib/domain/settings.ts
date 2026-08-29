// Engine-wide policy.

export interface Settings {
	maxConcurrentRuns: number;
	maxEnvironments: number;
	destroyCompletedAfter: string;
	destroyAbandoned: string;
	/** How long never-claimed uploads survive. */
	orphanUploadsAfter: string;
	/** Whether/when snapshot CONTENT expires ('never' or a duration); the
	 *  record keeps name/sha/size either way. */
	snapshotsAfter: string;
}
