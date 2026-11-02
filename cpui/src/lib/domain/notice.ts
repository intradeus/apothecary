// A setup problem a person must act on (a failing login, channel or tracker),
// kept until they dismiss it. Unlike a ping it belongs to no run.
export interface Notice {
	id: string;
	kind: string;
	title: string;
	body: string;
	/** A CPUI path where it can be fixed ('' = none). */
	link: string;
	/** How many times it was raised while open. */
	count: number;
	createdAt: string;
	lastSeenAt: string;
}
