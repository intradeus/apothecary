- 7) redo everything in clean architecture
- 23) new run on observability (full flow data + resource stats)
- 36) Resquash migrations
- 37) frontend rewrite. full tested suite.
- 39) code-server extensions for harnesses > if we use claude, it should also install the claude extension, codex > codex extension. omp has none. And those should be connected right ? Since we're already logged into the cli tool. 
- 40) Add a new flow to builder : connect git branch to jira ticket. So jira gets the branch context, we already have jira ticket field writes setup, not sure if a git branch connect is the same. As well as automatic branch naming, from parameters (remote ticket, bug type etc.)
- 41) performance check : how will this perform with 100 parallel runs ? 
- 43) SECURITY
- 44) egress improvements : block by default but add a mark as reviewed for blocks, so it doesnt ask a 2nd time. - maybe keep it as (refused) in orange, in between allow and block.
- 45) 

------ 
After 1.0.0 : 
- 39) RAW, durable workspaces. For fully hands-on work. Connectable via SSH using remote VScode. Goal : no more local compute. Apotehcary provisions the workspace as usual, but no flow is ever started. User gets either a 'connect to code-server' or a URL to ssh into it with desktop vscode. Extensions and harnesses are already installed, so user can still use AI, but just more manually, with remote compute AND everything should still work in the background after closing desktop vscode. Either this is a new non-flow feature OR we add a flow type where main only takes : manual and that's it. There's no end to it. But in a more complex flow, from the task's source and the branch created (as always), apothecary can still poll and get any MR related and close the workspace once the MR is merged, and move the tickets etc. BASICALLY just work on a "manual" (or "semi-automated") flow, with 0 agent stages, only human coding.
