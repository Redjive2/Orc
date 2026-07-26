# §1 Macmuffin

Macmuffin (`muff`) is a task tracker. It allows agents to create, claim, finish,
and manage tasks.

It is minimal, and highly focused. It lets agents set their scope (a file/path
list) and break up into subtasks arranged in groups as steps. This makes
workflows highly readable while they're going on, to those outside the workflow.

Scope is enforced rather than advisory: while a task is in force, editing is
limited to the files in its scope, including editing through Anno.

Tasks live in a shared pool, where any agent can see them and claim unclaimed
work.

User accounts are controlled via Orc.
