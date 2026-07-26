Authority levels, fine-grained permissions, and roles are how Orc manages both access control and purpose.

Each permission has a specific authority level.
The user has authority level 100. All other authority levels must be in range 1-99.

A given permission can include any number of specific commands or command patterns, and only those at or above its permission level can have that permission (but they *may* not, for whatever reason).

Permissions are assigned to roles, which lets anyone with that role perform those actions.

The builtin permissions are read(path list), write(path list), and spawn(agent load) where agent load is a function of model, effort, and # of models active.

Additionally, permissions can be granted directly (if temporarily) to an agent.

While all agents are listed flatly, they have a specific tree: each agent is a subagent of either the user or another subagent. A subagent can only have as high of a permission as their 'boss'.

All agents are able to move, fire, employ, and otherwise act on their subagents without need for permissions. They *do* need permissions to add more agent load to the worklist.

The worklist is the flat list referenced before: it holds all currently employed identities, and automatically populates them with their requested models and efforts.